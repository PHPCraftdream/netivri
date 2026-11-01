//go:build e2e

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

type exitDirectRecorder struct {
	listener net.Listener
	backend  string
	count    atomic.Int64
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func startE2EExitDirectRecorder(t *testing.T, backend string) *exitDirectRecorder {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("recorder listen:", err)
	}
	r := &exitDirectRecorder{listener: ln, backend: backend, conns: make(map[net.Conn]struct{})}
	acceptDone := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.conns[c] = struct{}{}
			r.mu.Unlock()
			r.wg.Add(1)
			go r.serve(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-acceptDone
		r.mu.Lock()
		for c := range r.conns {
			_ = c.Close()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
	return r
}

func (r *exitDirectRecorder) serve(c net.Conn) {
	defer r.wg.Done()
	defer func() { r.mu.Lock(); delete(r.conns, c); r.mu.Unlock(); _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	var h [2]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return
		}
		b := make([]byte, int(n[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	var pb [2]byte
	if _, err := io.ReadFull(c, pb[:]); err != nil {
		return
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:]))))
	up, err := net.DialTimeout("tcp", dest, 5*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Now().Add(20 * time.Second))
	if dest == r.backend {
		r.count.Add(1)
	}
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
}

func TestE2EExitDirectServerAccountRouting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	backend := accountsEcho(t)
	recorder := startE2EExitDirectRecorder(t, backend)
	const alicePass, bobPass = "exit-direct-alice-password", "exit-direct-bob-password"
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob password")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "route"}, {Login: "bob", Password: bobHash, Exit: "direct"}}
		cfg.Subscriptions = []config.Subscription{{Name: "route", URL: "http://route.invalid/sub"}}
	})
	accountsSaveSnapshot(t, homeA, "route", []subscription.Node{e2eExitDirectSocksNode("exit-direct-node", recorder.listener.Addr().String())})
	serverAngel := startUp(t, homeA, "--port", "0")
	t.Cleanup(func() {
		serverAngel.Stop()
		assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), alicePass, bobPass, aliceHash, bobHash)
	})
	port := freePort(t)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "vmess", "--name", "exit-direct-edge", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 1)
	waitServerStatus(t, ctx, homeA, true)
	link := func(account string) string {
		t.Helper()
		code, stdout, stderr := serverRun(t, homeA, "link", "exit-direct-edge", "--account", account, "--format", "uri")
		if code != 0 {
			t.Fatalf("server link: %s", serverErrorSummary(stdout, stderr))
		}
		var e struct {
			Data struct {
				Link string `json:"link"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(stdout), &e) != nil || e.Data.Link == "" {
			t.Fatal("server link omitted URI")
		}
		return e.Data.Link
	}
	links := map[string]string{"alice": link("alice"), "bob": link("bob")}
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		account := account
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, links[account]) })
	}
	sub := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("subscription listener:", err)
	}
	subDone := make(chan struct{})
	go func() { defer close(subDone); _ = sub.Serve(subListener) }()
	t.Cleanup(func() {
		_ = sub.Close()
		select {
		case <-subDone:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	homeB := t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		code, out, stderr = accountsRun(t, homeB, "sub", "add", "http://"+subListener.Addr().String()+"/"+account, "--name", account+"-edge")
		if code != 0 {
			t.Fatalf("import %s: %s", account, serverErrorSummary(out, stderr))
		}
	}
	writeConfig(t, homeB, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "alice-edge"}, {Login: "bob", Password: bobHash, Provider: "bob-edge"}}
	})
	clientAngel := startUp(t, homeB, "--port", "0")
	t.Cleanup(func() {
		clientAngel.Stop()
		assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), alicePass, bobPass, aliceHash, bobHash)
	})
	before := recorder.count.Load()
	accountsClient(t, clientAngel.Status().Data.Listen, "alice", alicePass, backend)
	if recorder.count.Load() <= before {
		t.Fatal("Alice did not use A route recorder")
	}
	before = recorder.count.Load()
	accountsClient(t, clientAngel.Status().Data.Listen, "bob", bobPass, backend)
	if recorder.count.Load() != before {
		t.Fatal("Bob direct traffic reached A's route recorder")
	}

	held := e2eExitDirectOpenVMess(t, links["bob"], backend)
	defer held.Close()
	e2eExitDirectSendExpect(t, held, "established-before-reload")
	code, out, stderr = accountsRun(t, homeA, "account", "set-exit", "bob", "route")
	if code != 0 {
		t.Fatalf("set-exit route: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 2)
	// No provider/country means route mode follows A's default DIRECT route.
	e2eExitDirectSendExpect(t, held, "open-after-default-route")
	before = recorder.count.Load()
	accountsClient(t, clientAngel.Status().Data.Listen, "bob", bobPass, backend)
	if recorder.count.Load() != before {
		t.Fatal("empty route did not use default direct exit")
	}

	code, out, stderr = accountsRun(t, homeA, "account", "set-provider", "bob", "route")
	if code != 0 {
		t.Fatalf("set-provider route: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 3)
	e2eExitDirectSendExpect(t, held, "open-after-provider-route")
	before = recorder.count.Load()
	accountsClient(t, clientAngel.Status().Data.Listen, "bob", bobPass, backend)
	if recorder.count.Load() <= before {
		t.Fatal("Bob routed mode missed A recorder")
	}
	code, out, stderr = accountsRun(t, homeA, "account", "set-exit", "bob", "direct")
	if code == 0 || decodeErrEnvelope(t, stderr).Error.Code != "CONFIG_INVALID" {
		t.Fatalf("direct with provider should fail: %s", serverErrorSummary(out, stderr))
	}
	cfg := loadConfigFile(t, homeA)
	bob, _ := cfg.Account("bob")
	if bob == nil || bob.Provider != "route" || bob.Exit != "route" {
		t.Fatal("failed direct validation mutated Bob")
	}
	// Remove the redundant set-exit route: Bob is already route. Explicitly
	// clear the provider then apply direct; each effective update increments once.
	if code, out, stderr = accountsRun(t, homeA, "account", "set-provider", "bob", "--none"); code != 0 {
		t.Fatalf("clear Bob provider: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 4)
	if code, out, stderr = accountsRun(t, homeA, "account", "set-exit", "bob", "direct"); code != 0 {
		t.Fatalf("set-exit direct: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 5)
	e2eExitDirectSendExpect(t, held, "open-after-direct")
	before = recorder.count.Load()
	accountsClient(t, clientAngel.Status().Data.Listen, "bob", bobPass, backend)
	if recorder.count.Load() != before {
		t.Fatal("Bob direct mode still used A recorder")
	}
	_ = held.Close()

	// CLI validation accounts are created only after connection-survival checks;
	// account additions restart the listener and intentionally end old sessions.
	serverAngel.Stop()
	for _, args := range [][]string{{"add", "exit-direct-provider", "--provider", "route"}, {"add", "exit-direct-country", "--country", "DE"}, {"add", "exit-direct-pre", "--pre-socks5", "socks5://127.0.0.1:1080"}} {
		code, out, stderr = accountsRun(t, homeA, append([]string{"account"}, args...)...)
		if code != 0 {
			t.Fatalf("setup validation account: %s", serverErrorSummary(out, stderr))
		}
	}
	checkCLIError := func(args []string, want string) {
		t.Helper()
		code, stdout, stderr := accountsRun(t, homeA, append([]string{"account"}, args...)...)
		if code == 0 {
			t.Fatalf("account %v unexpectedly succeeded", args)
		}
		env := decodeErrEnvelope(t, stderr)
		if env.Error.Code != want {
			t.Fatalf("account %v code=%s want %s stdout=%s", args, env.Error.Code, want, stdout)
		}
	}
	checkCLIError([]string{"set-exit", "bob", "unknown"}, "CONFIG_INVALID")
	checkCLIError([]string{"set-exit", "missing", "direct"}, "ACCOUNT_NOT_FOUND")
	checkCLIError([]string{"set-exit", "bad/name", "direct"}, "ACCOUNT_INVALID")
	checkCLIError([]string{"set-exit", "bob", "direct", "extra"}, "USAGE")
	checkCLIError([]string{"set-exit", "--all", "direct", "extra"}, "USAGE")
	checkCLIError([]string{"set-exit", "exit-direct-provider", "direct"}, "CONFIG_INVALID")
	checkCLIError([]string{"set-exit", "exit-direct-country", "direct"}, "CONFIG_INVALID")
	checkCLIError([]string{"set-exit", "exit-direct-pre", "direct"}, "CONFIG_INVALID")
	code, out, stderr = accountsRun(t, homeA, "account", "set-country", "alice", "ZZ")
	if code == 0 || decodeErrEnvelope(t, stderr).Error.Code != "NO_NODES_FOR_COUNTRY" {
		t.Fatalf("set-country alice ZZ should fail before write: %s", serverErrorSummary(out, stderr))
	}
	cfg = loadConfigFile(t, homeA)
	alice, _ := cfg.Account("alice")
	bob, _ = cfg.Account("bob")
	if alice == nil || alice.ExitCountry != "" || alice.Provider != "route" {
		t.Fatal("pre-write country validation mutated Alice")
	}
	if bob == nil || bob.Exit != "direct" || bob.Provider != "" {
		t.Fatal("failed CLI validation mutated Bob")
	}
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
}

func e2eExitDirectSocksNode(id, addr string) subscription.Node {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic(err)
	}
	return subscription.Node{ID: id, Name: id, Type: "socks5", Server: host, Port: port, Raw: map[string]any{"name": id, "type": "socks5", "server": host, "port": port}}
}

func e2eExitDirectOpenVMess(t *testing.T, uri, target string) net.Conn {
	t.Helper()
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 {
		t.Fatal("parse Bob VMess link")
	}
	n := nodes[0]
	uuid, _ := n.Raw["uuid"].(string)
	cipher, _ := n.Raw["cipher"].(string)
	client, err := outbound.NewVmess(outbound.VmessOption{Name: "exit-direct-established", Server: n.Server, Port: n.Port, UUID: uuid, AlterID: 0, Cipher: cipher, Network: "tcp"})
	if err != nil {
		t.Fatal("construct VMess client:", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(p)})
	if err != nil {
		t.Fatal("open VMess connection:", err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Minute)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return conn
}

func e2eExitDirectSendExpect(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatalf("write established tunnel: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read established tunnel: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("established echo=%q want %q", got, payload)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
}
