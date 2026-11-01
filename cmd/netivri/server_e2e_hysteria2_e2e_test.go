//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
		defer func() {
			if t.Failed() {
				e2eHysteria2Diagnostics(t, homeA, homeB, secrets)
			}
		}()
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
		e2eHysteria2Failure(t, homeA, homeB, secrets, "wrong credential", err)
	}
	if err := e2eHysteria2ProbeFromURI(ctx, oldAlice, aliceEcho, ""); err != nil {
		e2eHysteria2Failure(t, homeA, homeB, secrets, "valid imported URI", err)
	}
	beforeAlice, beforeBob := aliceUp.count.Load(), bobUp.count.Load()
	code, out, stderr = runServer("rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 2)
	if err := e2eHysteria2ProbeFromURI(ctx, oldAlice, aliceEcho, ""); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication failed, status code: 404") {
		e2eHysteria2Failure(t, homeA, homeB, secrets, "rotated credential", err)
	}
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() != beforeBob {
		e2eHysteria2Failure(t, homeA, homeB, secrets, "failed authentication route recorder", io.ErrUnexpectedEOF)
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
		e2eHysteria2Failure(t, homeA, homeB, secrets, "restricted Alice probe", nil)
	}
	e2eHysteria2WaitDropped(t, ctx, homeA, "edge")
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() != beforeBob {
		e2eHysteria2Failure(t, homeA, homeB, secrets, "restricted route recorder", io.ErrUnexpectedEOF)
	}
	// A dead route backs off probes (45s base, lengthened per failure); restarting B clears its cached dead-node state.
	restartB := func() {
		if code, stdout, errout := accountsRun(t, homeB, "down"); code != 0 && !e2eHysteria2AngelNotRunning(stdout, errout) {
			t.Fatalf("client angel down: %s", serverErrorSummary(stdout, errout))
		}
		angelB.Stop()
		angelB = startUp(t, homeB, "--port", "0")
		trackAngel(angelB)
	}
	restartB()
	e2eHysteria2RetryEchoes(t, ctx, angelB.Status().Data.Listen, accountPassword, secrets, homeA, homeB, []e2eHysteria2Echo{{"bob", bobEcho, bobUp, aliceUp}})
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear allowed clients: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 4)
	restartB()
	e2eHysteria2RetryEchoes(t, ctx, angelB.Status().Data.Listen, accountPassword, secrets, homeA, homeB, []e2eHysteria2Echo{{"alice", aliceEcho, aliceUp, bobUp}, {"bob", bobEcho, bobUp, aliceUp}})
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
	restartB()
	if link("alice") != aliceLink || link("bob") != bobLink {
		t.Fatal("server links changed across restart")
	}
	e2eHysteria2RetryEchoes(t, ctx, angelB.Status().Data.Listen, accountPassword, secrets, homeA, homeB, []e2eHysteria2Echo{{"alice", aliceEcho, aliceUp, bobUp}, {"bob", bobEcho, bobUp, aliceUp}})
	code, out, stderr = runServer("remove", "edge")
	if code != 0 {
		t.Fatalf("server remove: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eHysteria2WaitStatus(t, ctx, homeA, "edge", "hysteria2", false)
	e2eHysteria2WaitUDPFree(t, ctx, port)
}

type e2eHysteria2Echo struct {
	account string
	echo    string
	own     *accountsUpstream
	other   *accountsUpstream
}

func e2eHysteria2RetryEchoes(t *testing.T, parent context.Context, proxy, password string, secrets []string, homeA, homeB string, checks []e2eHysteria2Echo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	for _, check := range checks {
		beforeOwn, beforeOther := check.own.count.Load(), check.other.count.Load()
		var lastErr error
		for {
			attemptCtx, attemptCancel := context.WithTimeout(ctx, 3*time.Second)
			lastErr = e2eHysteria2AccountsClient(attemptCtx, proxy, check.account, password, check.echo)
			attemptCancel()
			if lastErr == nil {
				if check.own.count.Load() <= beforeOwn || check.other.count.Load() != beforeOther {
					e2eHysteria2Diagnostics(t, homeA, homeB, secrets)
					t.Fatalf("%s routed to wrong recorder", check.account)
				}
				break
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				e2eHysteria2Diagnostics(t, homeA, homeB, secrets)
				t.Fatalf("%s echo retries exhausted: %s", check.account, e2eHysteria2Sanitize(lastErr.Error(), secrets))
			case <-timer.C:
			}
		}
	}
}

func e2eHysteria2AccountsClient(ctx context.Context, proxy, user, pass, target string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil {
		return err
	}
	if method != [2]byte{5, 2} {
		return fmt.Errorf("SOCKS method %v", method)
	}
	if len(user) > 255 || len(pass) > 255 {
		return fmt.Errorf("credentials too long")
	}
	requestAuth := append([]byte{1, byte(len(user))}, []byte(user)...)
	requestAuth = append(requestAuth, byte(len(pass)))
	requestAuth = append(requestAuth, []byte(pass)...)
	if _, err = conn.Write(requestAuth); err != nil {
		return err
	}
	var authReply [2]byte
	if _, err = io.ReadFull(conn, authReply[:]); err != nil {
		return err
	}
	if authReply[1] != 0 {
		return fmt.Errorf("SOCKS auth reply %v", authReply)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return fmt.Errorf("target is not IPv4: %s", target)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return err
	}
	request := append([]byte{5, 1, 0, 1}, ip...)
	request = append(request, byte(port>>8), byte(port))
	if _, err = conn.Write(request); err != nil {
		return err
	}
	var reply [10]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[1] != 0 {
		return fmt.Errorf("SOCKS CONNECT reply %v", reply)
	}
	payload := []byte("hysteria2-route-check")
	if _, err = conn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func e2eHysteria2Failure(t *testing.T, homeA, homeB string, secrets []string, what string, err error) {
	t.Helper()
	e2eHysteria2Diagnostics(t, homeA, homeB, secrets)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded", what)
	}
	t.Fatalf("%s failed: %s", what, e2eHysteria2Sanitize(err.Error(), secrets))
}

func e2eHysteria2Diagnostics(t *testing.T, homeA, homeB string, secrets []string) {
	t.Helper()
	e2eHysteria2Diagnostic(t, homeA, false, secrets)
	e2eHysteria2Diagnostic(t, homeB, true, secrets)
}

func e2eHysteria2Diagnostic(t *testing.T, home string, full bool, secrets []string) {
	t.Helper()
	args := []string{"status", "--json"}
	if full {
		args = []string{"status", "--full", "--json"}
	}
	deadline := time.NewTimer(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
statusLoop:
	for {
		code, stdout, stderr, err := angelExec(home, args...)
		if err == nil && code == 0 {
			t.Logf("diagnostic status (%s): %s", home, e2eHysteria2Status(stdout, full, secrets))
			break
		}
		if !e2eHysteria2AngelNotRunning(stdout, stderr) {
			t.Logf("diagnostic status (%s): %s", home, e2eHysteria2Sanitize(stderr, secrets))
			break
		}
		select {
		case <-deadline.C:
			t.Logf("diagnostic status (%s): %s", home, e2eHysteria2Sanitize(stderr, secrets))
			break statusLoop
		case <-ticker.C:
		}
	}
}

func e2eHysteria2Status(raw string, full bool, secrets []string) string {
	var envelope struct {
		Data struct {
			Servers []struct {
				Name        string `json:"name"`
				Protocol    string `json:"protocol"`
				Connections int    `json:"connections"`
				Dropped     uint64 `json:"dropped_clients"`
			} `json:"servers"`
			Warm struct {
				Alive         int    `json:"alive"`
				Total         int    `json:"total"`
				Probes        uint64 `json:"probes"`
				ProbeFailures uint64 `json:"probe_failures"`
				PoolDiscards  uint64 `json:"pool_discards"`
				Nodes         []struct {
					Label   string `json:"label"`
					Name    string `json:"name"`
					Alive   bool   `json:"alive"`
					DelayMS int64  `json:"delay_ms"`
				} `json:"nodes"`
			} `json:"warm"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return "status JSON unavailable"
	}
	data, _ := json.Marshal(envelope.Data.Servers)
	out := "servers=" + string(data)
	if full {
		nodes, _ := json.Marshal(envelope.Data.Warm.Nodes)
		out += fmt.Sprintf(" warm.alive=%d warm.total=%d warm.probes=%d warm.probe_failures=%d warm.pool_discards=%d nodes.alive=%s", envelope.Data.Warm.Alive, envelope.Data.Warm.Total, envelope.Data.Warm.Probes, envelope.Data.Warm.ProbeFailures, envelope.Data.Warm.PoolDiscards, nodes)
	}
	return e2eHysteria2Sanitize(out, secrets)
}

func e2eHysteria2Sanitize(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.Contains(line, "://") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func e2eHysteria2AngelNotRunning(out, stderr string) bool {
	for _, raw := range []string{out, stderr} {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Error.Code == "ANGEL_NOT_RUNNING" {
			return true
		}
	}
	return false
}

func e2eHysteria2WaitStatus(t *testing.T, ctx context.Context, home, name, protocol string, present bool) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(t, home, "status")
		if code != 0 {
			if e2eHysteria2AngelNotRunning(out, stderr) {
				select {
				case <-ctx.Done():
					t.Fatalf("server status presence did not become %t", present)
				case <-ticker.C:
				}
				continue
			}
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
			if e2eHysteria2AngelNotRunning(out, stderr) {
				select {
				case <-ctx.Done():
					t.Fatal("allowed-client drop was not counted")
				case <-ticker.C:
				}
				continue
			}
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
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(destinationPort)})
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, ok := probeCtx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
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
