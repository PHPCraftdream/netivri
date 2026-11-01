//go:build e2e

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
)

var e2eVlessRealityNetworkRejected = errors.New("VLESS authentication rejected")

func TestE2EVlessRealityLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const pass = "e2e-vless-reality-account-password"
	aliceNode, bobNode := accountsServer(t), accountsServer(t)
	backend := accountsEcho(t)
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash account password")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Listen.AllowedClients = "127.0.0.0/8"
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "alice-route"}, {Login: "bob", Password: hash, Provider: "bob-route"}}
		cfg.Subscriptions = []config.Subscription{{Name: "alice-route", URL: "http://alice.invalid/sub"}, {Name: "bob-route", URL: "http://bob.invalid/sub"}}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-node", "", aliceNode)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-node", "", bobNode)})
	camouflage, stopCamouflage := e2eVlessRealityCamouflage(t)
	t.Cleanup(stopCamouflage)
	port := freePort(t)
	server := startUp(t, homeA, "--port", "0")
	serverProcesses := []*upProc{server}
	clientProcesses := []*upProc{}
	linkSecrets := []string{}
	t.Cleanup(func() {
		for _, p := range serverProcesses {
			p.Stop()
		}
		for _, p := range clientProcesses {
			p.Stop()
		}
		for _, p := range serverProcesses {
			assertNoSecrets(t, p.Stdout(), p.Stderr(), append([]string{pass, hash}, linkSecrets...)...)
		}
		for _, p := range clientProcesses {
			assertNoSecrets(t, p.Stdout(), p.Stderr(), append([]string{pass, hash}, linkSecrets...)...)
		}
	})
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "vless-reality", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "dest="+camouflage, "--param", "sni=e2e-reality.test")
	if code != 0 {
		t.Fatalf("server add: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, pass, hash)
	waitStatusReload(t, ctx, homeA, 1)
	e2eVlessRealityWaitStatus(t, ctx, homeA, true)
	getLinks := func() map[string]string {
		links := map[string]string{}
		for _, login := range []string{"alice", "bob"} {
			code, out, stderr = serverRun(t, homeA, "link", "edge", "--account", login, "--format", "uri")
			if code != 0 {
				t.Fatalf("server link: %s", serverErrorSummary(out, stderr))
			}
			var env struct {
				Data struct {
					Link string `json:"link"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
				t.Fatal("link response missing URI")
			}
			links[login] = env.Data.Link
			secrets := e2eVlessRealityLinkSecrets(t, map[string]string{login: env.Data.Link})
			assertNoSecrets(t, "", stderr, append([]string{pass, hash}, secrets...)...)
		}
		return links
	}
	links := getLinks()
	linkSecrets = append(linkSecrets, e2eVlessRealityLinkSecrets(t, links)...)
	var subMu sync.RWMutex
	subBodies := map[string][]byte{"alice": []byte(links["alice"] + "\n"), "bob": []byte(links["bob"] + "\n")}
	mux := http.NewServeMux()
	for _, login := range []string{"alice", "bob"} {
		login := login
		mux.HandleFunc("/"+login, func(w http.ResponseWriter, _ *http.Request) {
			subMu.RLock()
			defer subMu.RUnlock()
			_, _ = w.Write(subBodies[login])
		})
	}
	subServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen subscription")
	}
	subDone := make(chan struct{})
	go func() { defer close(subDone); _ = subServer.Serve(subListener) }()
	t.Cleanup(func() {
		_ = subServer.Close()
		select {
		case <-subDone:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	homeB := t.TempDir()
	for _, login := range []string{"alice", "bob"} {
		code, out, stderr = accountsRun(t, homeB, "sub", "add", "http://"+subListener.Addr().String()+"/"+login, "--name", "edge-"+login)
		if code != 0 {
			t.Fatalf("subscription add: %s", serverErrorSummary(out, stderr))
		}
	}
	savedB := loadConfigFile(t, homeB)
	writeConfig(t, homeB, func(cfg *config.Config) {
		cfg.Subscriptions = savedB.Subscriptions
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "edge-alice"}, {Login: "bob", Password: hash, Provider: "edge-bob"}}
	})
	clientAngel := startUp(t, homeB, "--port", "0")
	clientProcesses = append(clientProcesses, clientAngel)
	drive := func(login string) {
		t.Helper()
		a, b := aliceNode.count.Load(), bobNode.count.Load()
		accountsClient(t, clientAngel.Status().Data.Listen, login, pass, backend)
		if login == "alice" {
			if aliceNode.count.Load() <= a || bobNode.count.Load() != b {
				t.Fatal("Alice route recorder mismatch")
			}
		} else if bobNode.count.Load() <= b || aliceNode.count.Load() != a {
			t.Fatal("Bob route recorder mismatch")
		}
	}
	drive("alice")
	drive("bob")
	probe := func(uri string, wrong bool) error {
		nodes, e := subscription.Parse([]byte(uri))
		if e != nil || len(nodes) != 1 {
			t.Fatal("generated link failed subscription parsing")
		}
		raw := make(map[string]any, len(nodes[0].Raw))
		for k, v := range nodes[0].Raw {
			raw[k] = v
		}
		if wrong {
			raw["uuid"] = "00000000-0000-4000-8000-000000000001"
		}
		proxy, e := adapter.ParseProxy(raw)
		if e != nil {
			t.Fatal("generated VLESS link failed Mihomo client parsing")
		}
		client := proxy.Adapter()
		defer client.Close()
		host, portText, e := net.SplitHostPort(backend)
		if e != nil {
			t.Fatal("backend address invalid")
		}
		ip, e := netip.ParseAddr(host)
		if e != nil {
			t.Fatal("backend IP invalid")
		}
		n, e := strconv.Atoi(portText)
		if e != nil || n < 1 || n > 65535 {
			t.Fatal("backend port invalid")
		}
		probeCtx, stop := context.WithTimeout(ctx, 8*time.Second)
		defer stop()
		conn, e := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: ip, DstPort: uint16(n)})
		if e != nil {
			if wrong && probeCtx.Err() == nil && e2eVlessRealityIsAuthRejection(e) {
				return e2eVlessRealityNetworkRejected
			}
			return fmt.Errorf("network connection failed")
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		payload := "vless-reality-probe"
		if _, e = io.WriteString(conn, payload); e != nil {
			if wrong && e2eVlessRealityIsAuthRejection(e) {
				return e2eVlessRealityNetworkRejected
			}
			return fmt.Errorf("network write failed")
		}
		got := make([]byte, len(payload))
		if _, e = io.ReadFull(conn, got); e != nil {
			if wrong && e2eVlessRealityIsAuthRejection(e) {
				return e2eVlessRealityNetworkRejected
			}
			return fmt.Errorf("network read failed")
		}
		if string(got) != payload {
			return fmt.Errorf("unexpected echo")
		}
		return nil
	}
	beforeAlice, beforeBob := aliceNode.count.Load(), bobNode.count.Load()
	if err := waitServerPort(ctx, port, true); err != nil {
		t.Fatal("VLESS server port did not open")
	}
	if err := probe(links["alice"], true); !errors.Is(err, e2eVlessRealityNetworkRejected) {
		t.Fatal("wrong UUID was not rejected at the network authentication stage")
	}
	if aliceNode.count.Load() != beforeAlice || bobNode.count.Load() != beforeBob {
		t.Fatal("wrong UUID probe reached account recorder")
	}
	if err := probe(links["alice"], false); err != nil {
		t.Fatal("valid Reality probe failed")
	}
	if aliceNode.count.Load() <= beforeAlice || bobNode.count.Load() != beforeBob {
		t.Fatal("valid Alice probe used wrong route")
	}
	code, out, stderr = serverRun(t, homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, append([]string{pass, hash}, linkSecrets...)...)
	waitStatusReload(t, ctx, homeA, 2)
	beforeAlice, beforeBob = aliceNode.count.Load(), bobNode.count.Load()
	if err := probe(links["alice"], false); err == nil {
		t.Fatal("old link worked after rotation")
	}
	if aliceNode.count.Load() != beforeAlice || bobNode.count.Load() != beforeBob {
		t.Fatal("old link reached account recorder")
	}
	links = getLinks()
	linkSecrets = append(linkSecrets, e2eVlessRealityLinkSecrets(t, links)...)
	subMu.Lock()
	subBodies["alice"] = []byte(links["alice"] + "\n")
	subBodies["bob"] = []byte(links["bob"] + "\n")
	subMu.Unlock()
	beforeReload := liveStatusOf(t, homeB).Data.Reload.Count
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "edge-alice")
	if code != 0 {
		t.Fatalf("subscription update: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeB, beforeReload+1)
	drive("alice")
	drive("bob")
	if err := probe(links["alice"], false); err != nil {
		t.Fatal("rotated VLESS link failed")
	}
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.1")
	if code != 0 {
		t.Fatalf("set account policy: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 3)
	beforeAlice, beforeBob = aliceNode.count.Load(), bobNode.count.Load()
	beforeDropped := e2eVlessRealityDropped(t, homeA)
	if err := probe(links["alice"], false); err == nil {
		t.Fatal("loopback Alice client was allowed")
	}
	if aliceNode.count.Load() != beforeAlice || bobNode.count.Load() != beforeBob {
		t.Fatal("disallowed Alice reached account recorder")
	}
	if e2eVlessRealityDropped(t, homeA) <= beforeDropped {
		t.Fatal("denial did not increment dropped-client count")
	}
	drive("bob")
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear policy: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 4)
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("down: %s", serverErrorSummary(out, stderr))
	}
	server.Stop()
	server = startUp(t, homeA, "--port", "0")
	serverProcesses = append(serverProcesses, server)
	if err := probe(links["alice"], false); err != nil {
		t.Fatal("persistent link failed after restart")
	}
	drive("alice")
	code, out, stderr = serverRun(t, homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("remove: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, append([]string{pass, hash}, linkSecrets...)...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eVlessRealityWaitStatus(t, ctx, homeA, false, linkSecrets...)
	if err := waitServerPort(ctx, port, false); err != nil {
		t.Fatalf("removed listener remains open: %v", err)
	}
}

func e2eVlessRealityIsAuthRejection(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && !networkErr.Timeout()
}

func e2eVlessRealityLinkSecrets(t *testing.T, links map[string]string) []string {
	t.Helper()
	var secrets []string
	for _, raw := range links {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal("parse generated link")
		}
		if u.User != nil {
			if value, ok := u.User.Password(); ok {
				secrets = append(secrets, value)
			}
			secrets = append(secrets, u.User.Username())
		}
		q := u.Query()
		for _, key := range []string{"pbk", "sid"} {
			if value := q.Get(key); value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	return secrets
}

func e2eVlessRealityWaitStatus(t *testing.T, ctx context.Context, home string, want bool, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(t, home, "status")
		if code != 0 {
			t.Fatalf("status failed: %s", serverErrorSummary(out, stderr))
		}
		var env struct {
			Data struct {
				Servers []struct {
					Name     string `json:"name"`
					Protocol string `json:"protocol"`
				} `json:"servers"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &env) != nil {
			t.Fatal("decode status")
		}
		present := false
		for _, s := range env.Data.Servers {
			if s.Name == "edge" {
				present = true
				if want && s.Protocol != "vless-reality" {
					t.Fatal("unexpected server protocol")
				}
			}
		}
		assertNoSecrets(t, out, stderr, append([]string{"e2e-vless-reality-account-password"}, secrets...)...)
		if present == want {
			return
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(t, home, "VLESS Reality status wait expired")
			t.Fatalf("server presence did not become %t", want)
		case <-ticker.C:
		}
	}
}

func e2eVlessRealityDropped(t *testing.T, home string) uint64 {
	t.Helper()
	code, out, stderr := accountsRun(t, home, "status")
	if code != 0 {
		t.Fatalf("status failed: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Servers []struct {
				Name    string `json:"name"`
				Dropped uint64 `json:"dropped_clients"`
			} `json:"servers"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil {
		t.Fatal("decode server status")
	}
	for _, s := range env.Data.Servers {
		if s.Name == "edge" {
			return s.Dropped
		}
	}
	t.Fatal("edge absent from status")
	return 0
}

func e2eVlessRealityCamouflage(t *testing.T) (string, func()) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("generate camouflage key")
	}
	dns := make([]string, 33)
	dns[0] = "e2e-reality.test"
	for i := 1; i < len(dns); i++ {
		dns[i] = fmt.Sprintf("e2e-reality-camouflage-%02d-%s.test", i, strings.Repeat("x", 40))
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e-reality.test"}, DNSNames: dns, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal("create camouflage certificate")
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	padding := make([]byte, 4096)
	if _, err = rand.Read(padding); err != nil {
		t.Fatal("create ticket padding")
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{"h2"}}
	cfg.WrapSession = func(cs tls.ConnectionState, state *tls.SessionState) ([]byte, error) {
		state.Extra = append(state.Extra, padding)
		return cfg.EncryptTicket(cs, state)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen TLS camouflage")
	}
	var workers sync.WaitGroup
	var conns sync.Map
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			tc := tls.Server(c, cfg)
			conns.Store(tc, struct{}{})
			workers.Add(1)
			go func(c *tls.Conn) {
				defer workers.Done()
				defer conns.Delete(c)
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				if c.Handshake() == nil {
					_, _ = io.Copy(io.Discard, c)
				}
			}(tc)
		}
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		select {
		case <-acceptDone:
		case <-time.After(e2eKillGrace):
			t.Error("camouflage accept loop did not stop")
		}
		conns.Range(func(k, _ any) bool { _ = k.(net.Conn).Close(); return true })
		finished := make(chan struct{})
		go func() { workers.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(e2eKillGrace):
			t.Error("camouflage TLS workers did not stop")
		}
	}
}
