//go:build e2e

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

const (
	accountsDEPass = "accounts-de-password"
	accountsNLPass = "accounts-nl-password"
)

type accountsUpstream struct {
	listener net.Listener
	count    atomic.Int64
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

func accountsServer(t *testing.T) *accountsUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &accountsUpstream{listener: ln, conns: make(map[net.Conn]struct{})}
	s.wg.Add(1)
	acceptDone := make(chan struct{})
	go func() {
		defer s.wg.Done()
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[c] = struct{}{}
			s.mu.Unlock()
			s.wg.Add(1)
			go s.serve(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-acceptDone
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

func (s *accountsUpstream) serve(c net.Conn) {
	defer s.wg.Done()
	defer func() { s.mu.Lock(); delete(s.conns, c); s.mu.Unlock(); _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	var greeting [2]byte
	if _, err := io.ReadFull(c, greeting[:]); err != nil {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 || req[1] != 1 || req[3] != 1 {
		return
	}
	var addr [6]byte
	if _, err := io.ReadFull(c, addr[:]); err != nil {
		return
	}
	target := net.JoinHostPort(net.IP(addr[:4]).String(), fmt.Sprint(binary.BigEndian.Uint16(addr[4:])))
	out, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer out.Close()
	_ = out.SetDeadline(time.Now().Add(20 * time.Second))
	s.count.Add(1)
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(out, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, out); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = out.Close()
	<-done
}

func accountsEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	conns := make(map[net.Conn]struct{})
	acceptDone := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns[c] = struct{}{}
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { mu.Lock(); delete(conns, c); mu.Unlock(); _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-acceptDone
		mu.Lock()
		for c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

func accountsClient(t *testing.T, proxy, user, pass, target string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	var method [2]byte
	if _, err = io.ReadFull(c, method[:]); err != nil || method != [2]byte{5, 2} {
		t.Fatalf("SOCKS method %v: %v", method, err)
	}
	if len(user) > 255 || len(pass) > 255 {
		t.Fatal("credentials too long")
	}
	if _, err = c.Write(append([]byte{1, byte(len(user))}, append(append([]byte(user), byte(len(pass))), []byte(pass)...)...)); err != nil {
		t.Fatal(err)
	}
	var authReply [2]byte
	if _, err = io.ReadFull(c, authReply[:]); err != nil || authReply[1] != 0 {
		t.Fatalf("auth reply %v: %v", authReply, err)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		t.Fatalf("target is not IPv4: %s", target)
	}
	var port uint16
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	request := []byte{5, 1, 0, 1}
	request = append(request, ip...)
	request = append(request, byte(port>>8), byte(port))
	if _, err = c.Write(request); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err = io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
		t.Fatalf("CONNECT reply %v: %v", reply, err)
	}
	payload := []byte("account-country-route")
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != string(payload) {
		t.Fatalf("echo %q: %v", got, err)
	}
}

func accountsCommand(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	full := append([]string{"account"}, args...)
	full = append(full, "--home", home, "--json")
	cmd := exec.CommandContext(ctx, netivriBin, full...)
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home)
	var out, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("account command timeout: %v", args)
	}
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("account command: %v", err)
		}
	}
	return code, out.String(), stderr.String()
}

func TestE2EAccountsRoutingAndCLI(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	de, err := auth.Hash(accountsDEPass)
	if err != nil {
		t.Fatal(err)
	}
	nl, err := auth.Hash(accountsNLPass)
	if err != nil {
		t.Fatal(err)
	}
	backend := accountsEcho(t)
	deUp := accountsServer(t)
	nlUp := accountsServer(t)
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: de, ExitCountry: "DE"}, {Login: "bob", Password: nl, ExitCountry: "NL"}}
		cfg.Subscriptions = []config.Subscription{{Name: "fixture", URL: "https://fixture.invalid/subscription"}}
	})
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	nodes := make([]subscription.Node, 0, 2)
	for _, item := range []struct {
		id, country string
		upstream    *accountsUpstream
	}{{"de-node", "DE", deUp}, {"nl-node", "NL", nlUp}} {
		host, portText, err := net.SplitHostPort(item.upstream.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		var port int
		_, _ = fmt.Sscanf(portText, "%d", &port)
		nodes = append(nodes, subscription.Node{ID: item.id, Name: item.id, Type: "socks5", Server: host, Port: port, Country: item.country, Raw: map[string]any{"name": item.id, "type": "socks5", "server": host, "port": port}})
	}
	if err := store.Save("fixture", subscription.Snapshot{FetchedAt: time.Now(), Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	p := startUp(t, home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	accountsClient(t, p.Status().Data.Listen, "alice", accountsDEPass, backend)
	if deUp.count.Load() != 1 || nlUp.count.Load() != 0 {
		t.Fatalf("upstream counts DE=%d NL=%d", deUp.count.Load(), nlUp.count.Load())
	}
	accountsClient(t, p.Status().Data.Listen, "bob", accountsNLPass, backend)
	if deUp.count.Load() != 1 || nlUp.count.Load() != 1 {
		t.Fatalf("upstream counts DE=%d NL=%d", deUp.count.Load(), nlUp.count.Load())
	}
	p.Stop()
	assertNoSecrets(t, p.Stdout(), p.Stderr(), accountsDEPass, accountsNLPass, de, nl)

	code, out, stderr := accountsCommand(t, home, "add", "carol", "--country", "DE")
	if code != 0 {
		t.Fatalf("account add: %s%s", out, stderr)
	}
	assertNoSecrets(t, out, stderr, accountsDEPass, accountsNLPass, de, nl)
	code, out, stderr = accountsCommand(t, home, "list")
	if code != 0 {
		t.Fatalf("account list: %s%s", out, stderr)
	}
	var listing struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(listing.Data), "carol") || !strings.Contains(string(listing.Data), "DE") {
		t.Fatalf("account list missing fields: %s", out)
	}
	assertNoSecrets(t, out, stderr, accountsDEPass, accountsNLPass, de, nl)
	accountsListed := func() string {
		code, out, stderr := accountsCommand(t, home, "list")
		if code != 0 {
			t.Fatalf("account list: %s%s", out, stderr)
		}
		assertNoSecrets(t, out, stderr, accountsDEPass, accountsNLPass, de, nl)
		return out
	}
	code, out, stderr = accountsCommand(t, home, "set-country", "carol", "NL")
	assertNoSecrets(t, out, stderr, accountsDEPass, accountsNLPass, de, nl)
	if code != 0 {
		t.Fatalf("account set-country: %s%s", out, stderr)
	}
	if listed := accountsListed(); !strings.Contains(listed, `"login":"carol","exit_country":"NL"`) {
		t.Fatalf("set-country not persisted: %s", listed)
	}
	code, out, stderr = accountsCommand(t, home, "set-country", "carol", "--none")
	assertNoSecrets(t, out, stderr, accountsDEPass, accountsNLPass, de, nl)
	if code != 0 {
		t.Fatalf("account set-country --none: %s%s", out, stderr)
	}
	if listed := accountsListed(); !strings.Contains(listed, `"login":"carol","exit_country":""`) {
		t.Fatalf("country not cleared: %s", listed)
	}
	code, out, stderr = accountsCommand(t, home, "rm", "carol")
	assertNoSecrets(t, out, stderr, accountsDEPass, accountsNLPass, de, nl)
	if code != 0 {
		t.Fatalf("account rm: %s%s", out, stderr)
	}
	if listed := accountsListed(); strings.Contains(listed, "carol") {
		t.Fatalf("account not removed: %s", listed)
	}
}

func TestE2EAccountsMissingCountryFailsClosed(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	hash, err := auth.Hash(accountsDEPass)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, ExitCountry: "DE"}}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runNetivri(t, home, "--port", port)
	if code != 5 {
		t.Fatalf("exit code = %d, want 5 (stderr %s)", code, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "NO_NODES_FOR_COUNTRY" {
		t.Errorf("error code = %q, want NO_NODES_FOR_COUNTRY", got)
	}
	assertNoSecrets(t, "", stderr, accountsDEPass, hash)
	if _, err := os.Stat(daemon.StatePath(home)); !os.IsNotExist(err) {
		t.Fatalf("daemon state path exists or could not be checked: %v", err)
	}
}
