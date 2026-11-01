//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func TestE2EHysteria2Lifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	const accountPassword = "hysteria2-e2e-account-password"
	aliceUp, bobUp := accountsServer(t), accountsServer(t)
	aliceEcho, bobEcho := accountsEcho(t), accountsEcho(t)
	hash, err := auth.Hash(accountPassword)
	if err != nil {
		t.Fatal("hash account password")
	}
	secrets := []string{accountPassword, hash}
	homeA := t.TempDir()
	aliceURL := accountsSubServer(t, "placeholder")
	bobURL := accountsSubServer(t, "placeholder")
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "route-alice"}, {Login: "bob", Password: hash, Provider: "route-bob"}}
		cfg.Subscriptions = []config.Subscription{{Name: "route-alice", URL: aliceURL}, {Name: "route-bob", URL: bobURL}}
	})
	accountsSaveSnapshot(t, homeA, "route-alice", []subscription.Node{accountsSocksNode("alice-route", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "route-bob", []subscription.Node{accountsSocksNode("bob-route", "", bobUp)})
	angelA := startUp(t, homeA, "--port", "0")
	trackAngel := func(process *upProc) {
		t.Helper()
		t.Cleanup(func() {
			process.Stop()
			assertNoSecrets(t, process.Stdout(), process.Stderr(), secrets...)
		})
	}
	trackAngel(angelA)
	portConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve UDP port")
	}
	port := portConn.LocalAddr().(*net.UDPAddr).Port
	_ = portConn.Close()
	runServer := func(args ...string) (int, string, string) {
		t.Helper()
		return serverRun(t, homeA, args...)
	}
	code, out, stderr := runServer("add", "--protocol", "hysteria2", "--name", "edge", "--account", "*", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eHysteria2WaitStatus(t, ctx, homeA, "edge", "hysteria2", true)
	link := func(account string) string {
		t.Helper()
		c, stdout, errout := runServer("link", "edge", "--account", account, "--format", "uri")
		if c != 0 {
			t.Fatalf("server link: %s", serverErrorSummary(stdout, errout))
		}
		var envelope struct {
			Data struct {
				Link string `json:"link"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(stdout), &envelope) != nil || envelope.Data.Link == "" {
			t.Fatal("server link missing")
		}
		return envelope.Data.Link
	}
	var bodyMu sync.RWMutex
	bodyByAccount := map[string]string{}
	subServer := &http.Server{
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyMu.RLock()
			body := bodyByAccount[strings.TrimPrefix(r.URL.Path, "/")]
			bodyMu.RUnlock()
			_, _ = io.WriteString(w, body+"\n")
		}),
	}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("subscription listen")
	}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = subServer.Serve(subListener) }()
	t.Cleanup(func() {
		_ = subServer.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("subscription server did not stop")
		}
	})
	setLinks := func() {
		aliceLink, bobLink := link("alice"), link("bob")
		bodyMu.Lock()
		bodyByAccount["alice"], bodyByAccount["bob"] = aliceLink, bobLink
		bodyMu.Unlock()
		for _, uri := range []string{aliceLink, bobLink} {
			nodes, parseErr := subscription.Parse([]byte(uri))
			if parseErr != nil || len(nodes) != 1 {
				t.Fatal("parse server URI")
			}
			password, _ := nodes[0].Raw["password"].(string)
			if password != "" {
				secrets = append(secrets, password)
			}
		}
	}
	setLinks()
	baseURL := "http://" + subListener.Addr().String()
	homeB := t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		subURL := baseURL + "/" + account
		c, stdout, errout := accountsRun(t, homeB, "sub", "add", subURL, "--name", "provider-"+account)
		if c != 0 {
			t.Fatalf("subscription add: %s", serverErrorSummary(stdout, errout))
		}
		assertNoSecrets(t, stdout, errout, secrets...)
		c, stdout, errout = accountsRun(t, homeB, "account", "add", account, "--provider", "provider-"+account)
		if c != 0 {
			t.Fatalf("account add: %s", serverErrorSummary(stdout, errout))
		}
		assertNoSecrets(t, stdout, errout, secrets...)
	}
	cfgB := loadConfigFile(t, homeB)
	for i := range cfgB.Accounts {
		cfgB.Accounts[i].Password = hash
	}
	if err := config.Save(config.ConfigPath(homeB), cfgB); err != nil {
		t.Fatal("save claimed client accounts")
	}
	angelB := startUp(t, homeB, "--port", "0")
	trackAngel(angelB)
	drive := func(account, echo string, own, other *accountsUpstream) {
		t.Helper()
		beforeOwn, beforeOther := own.count.Load(), other.count.Load()
		accountsClient(t, angelB.Status().Data.Listen, account, accountPassword, echo)
		if own.count.Load() <= beforeOwn || other.count.Load() != beforeOther {
			t.Fatalf("%s routed to wrong recorder", account)
		}
	}
	drive("alice", aliceEcho, aliceUp, bobUp)
	drive("bob", bobEcho, bobUp, aliceUp)
	bodyMu.RLock()
	aliceURI := bodyByAccount["alice"]
	bodyMu.RUnlock()
	oldAlice := aliceURI
	if err := e2eHysteria2ProbeFromURI(ctx, aliceURI, aliceEcho, "incorrect-hysteria2-credential"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication failed, status code: 404") {
		t.Fatal("wrong credential did not produce Hysteria2 authentication failure")
	}
	if err := e2eHysteria2ProbeFromURI(ctx, oldAlice, aliceEcho, ""); err != nil {
		t.Fatal("valid imported Hysteria2 URI failed")
	}
	beforeAlice, beforeBob := aliceUp.count.Load(), bobUp.count.Load()
	code, out, stderr = runServer("rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 2)
	if err := e2eHysteria2ProbeFromURI(ctx, oldAlice, aliceEcho, ""); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication failed, status code: 404") {
		t.Fatal("rotated credential did not produce Hysteria2 authentication failure")
	}
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() != beforeBob {
		t.Fatal("failed authentication reached a route recorder")
	}
	setLinks()
	for _, account := range []string{"alice"} {
		c, stdout, errout := accountsRun(t, homeB, "sub", "update", "provider-"+account)
		if c != 0 {
			t.Fatalf("subscription update: %s", serverErrorSummary(stdout, errout))
		}
		assertNoSecrets(t, stdout, errout, secrets...)
	}
	waitStatusReload(t, ctx, homeB, 1)
	drive("alice", aliceEcho, aliceUp, bobUp)
	drive("bob", bobEcho, bobUp, aliceUp)
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("set allowed clients: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 3)
	beforeAlice, beforeBob = aliceUp.count.Load(), bobUp.count.Load()
	bodyMu.RLock()
	aliceURI = bodyByAccount["alice"]
	bodyMu.RUnlock()
	if err := e2eHysteria2ProbeFromURI(ctx, aliceURI, aliceEcho, ""); err == nil {
		t.Fatal("restricted Alice client unexpectedly succeeded")
	}
	e2eHysteria2WaitDropped(t, ctx, homeA, "edge")
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() != beforeBob {
		t.Fatal("restricted client reached a route recorder")
	}
	drive("bob", bobEcho, bobUp, aliceUp)
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear allowed clients: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 4)
	aliceLink, bobLink := link("alice"), link("bob")
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("angel down: %s", serverErrorSummary(out, stderr))
	}
	angelA.Stop()
	assertNoSecrets(t, angelA.Stdout(), angelA.Stderr(), secrets...)
	angelA = startUp(t, homeA, "--port", "0")
	trackAngel(angelA)
	e2eHysteria2WaitStatus(t, ctx, homeA, "edge", "hysteria2", true)
	if link("alice") != aliceLink || link("bob") != bobLink {
		t.Fatal("server links changed across restart")
	}
	drive("alice", aliceEcho, aliceUp, bobUp)
	drive("bob", bobEcho, bobUp, aliceUp)
	code, out, stderr = runServer("remove", "edge")
	if code != 0 {
		t.Fatalf("server remove: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eHysteria2WaitStatus(t, ctx, homeA, "edge", "hysteria2", false)
	e2eHysteria2WaitUDPFree(t, ctx, port)
}

func e2eHysteria2WaitStatus(t *testing.T, ctx context.Context, home, name, protocol string, present bool) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(t, home, "status")
		if code != 0 {
			t.Fatalf("status: %s", serverErrorSummary(out, stderr))
		}
		var envelope struct {
			Data struct {
				Servers []struct {
					Name        string `json:"name"`
					Protocol    string `json:"protocol"`
					Connections int    `json:"connections"`
					Dropped     int    `json:"dropped_clients"`
				} `json:"servers"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &envelope) != nil {
			t.Fatal("decode server status")
		}
		found := false
		for _, server := range envelope.Data.Servers {
			if server.Name == name {
				found = true
				if server.Protocol != protocol || server.Connections < 0 || server.Dropped < 0 {
					t.Fatal("invalid server status")
				}
			}
		}
		if found == present {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("server status presence did not become %t", present)
		case <-ticker.C:
		}
	}
}

func e2eHysteria2WaitDropped(t *testing.T, ctx context.Context, home, name string) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(t, home, "status")
		if code != 0 {
			t.Fatalf("status: %s", serverErrorSummary(out, stderr))
		}
		var envelope struct {
			Data struct {
				Servers []struct {
					Name    string `json:"name"`
					Dropped uint64 `json:"dropped_clients"`
				} `json:"servers"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatal("decode server status")
		}
		for _, server := range envelope.Data.Servers {
			if server.Name == name && server.Dropped > 0 {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("allowed-client drop was not counted")
		case <-ticker.C:
		}
	}
}

func e2eHysteria2WaitUDPFree(t *testing.T, ctx context.Context, port int) {
	t.Helper()
	for {
		conn, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("removed server did not release UDP port")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func e2eHysteria2ProbeFromURI(ctx context.Context, uri, target, overridePassword string) error {
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 || nodes[0].Type != "hysteria2" {
		return io.ErrUnexpectedEOF
	}
	raw := nodes[0].Raw
	password, _ := raw["password"].(string)
	fingerprint, _ := raw["fingerprint"].(string)
	sni, _ := raw["sni"].(string)
	skipVerify, _ := raw["skip-cert-verify"].(bool)
	if overridePassword != "" {
		password = overridePassword
	}
	if password == "" || fingerprint == "" || sni == "" || skipVerify {
		return io.ErrUnexpectedEOF
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	destinationPort, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	client, err := outbound.NewHysteria2(outbound.Hysteria2Option{Name: "e2e-hysteria2-probe", Server: nodes[0].Server, Port: nodes[0].Port, Password: password, SNI: sni, Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	defer client.Close()
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(destinationPort)})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	const payload = "hysteria2-route-check"
	if _, err = io.WriteString(conn, payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != payload {
		return io.ErrUnexpectedEOF
	}
	return nil
}
