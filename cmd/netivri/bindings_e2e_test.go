//go:build e2e

package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// bindingsUpstream is a socks5 upstream that counts, per CONNECT target, how
// many client connections it relayed.
type bindingsUpstream struct {
	ln     net.Listener
	mu     sync.Mutex
	relays map[string]int
	done   chan struct{}
}

func newBindingsUpstream(t *testing.T) *bindingsUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &bindingsUpstream{ln: ln, relays: map[string]int{}, done: make(chan struct{})}
	go u.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		<-u.done
	})
	return u
}

func (u *bindingsUpstream) serve() {
	defer close(u.done)
	for {
		c, err := u.ln.Accept()
		if err != nil {
			return
		}
		go u.handle(c)
	}
}

func (u *bindingsUpstream) handle(c net.Conn) {
	defer c.Close()
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
	u.mu.Lock()
	u.relays[target]++
	u.mu.Unlock()
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

// count returns how many client connections were relayed to the target.
func (u *bindingsUpstream) count(target string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.relays[target]
}

func bindingsSocksNode(id, country, server string, port int) subscription.Node {
	return subscription.Node{ID: id, Name: id, Type: "socks5", Server: server, Port: port, Country: country,
		Raw: map[string]any{"name": id, "type": "socks5", "server": server, "port": port}}
}

// bindingsEchoAll is an echo server on all local addresses, so the sites
// 127.0.0.1 and 127.0.0.2 are two distinct destinations for the same port.
func bindingsEchoAll(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// bindingsRetryUntil polls cond with a fixed short pause until it holds or
// the deadline passes.
func bindingsRetryUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not observed within 30s: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type bindingsStatus struct {
	Data struct {
		Listen   string `json:"listen"`
		Affinity *struct {
			Restored int `json:"restored"`
			Bindings int `json:"bindings"`
		} `json:"affinity"`
	} `json:"data"`
}

func bindingsStatusOf(t *testing.T, home string) bindingsStatus {
	t.Helper()
	code, out, stderr := accountsRun(t, home, "status", "--full")
	if code != 0 {
		t.Fatalf("status exit %d: %s%s", code, out, stderr)
	}
	var st bindingsStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return st
}

// TestE2EBindingsSurviveRestart proves that the persisted binding keeps a
// site's exit node across a graceful `down` and a later `up`.
//
// The first connection of the daemon creates a provisional binding (the
// route member still has unknown health), and provisional bindings are never
// persisted. A second connection to a DIFFERENT site is non-provisional,
// because its member's health became known through the first connection's
// dial (internal/core/affinity.go provisionalBinding). That second site is
// the one whose persistence the test asserts: two distinct IP-literal sites
// (127.0.0.1 and 127.0.0.2, the echo backend listens on 0.0.0.0), the same
// upstream, the same port. While the daemon is down the provider order is
// flipped, so a fresh pick would prefer the other provider's node; only the
// stored binding can keep the first node.
func TestE2EBindingsSurviveRestart(t *testing.T) {
	h := newDaemonHome(t)
	home := h.dir
	pass := "bindings-e2e-password"
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal(err)
	}
	backend := bindingsEchoAll(t)
	_, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	var backendPort int
	if _, err := fmt.Sscanf(backendPortText, "%d", &backendPort); err != nil {
		t.Fatal(err)
	}
	site1 := net.JoinHostPort("127.0.0.1", backendPortText)
	site2 := net.JoinHostPort("127.0.0.2", backendPortText)
	up1 := newBindingsUpstream(t)
	up2 := newBindingsUpstream(t)
	nodeFor := func(up *bindingsUpstream, id string) subscription.Node {
		host, portText, err := net.SplitHostPort(up.ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		var port int
		if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
			t.Fatal(err)
		}
		return bindingsSocksNode(id, "DE", host, port)
	}
	accountsSaveSnapshot(t, home, "p1", []subscription.Node{nodeFor(up1, "de-old")})
	accountsSaveSnapshot(t, home, "p2", []subscription.Node{nodeFor(up2, "de-p2")})
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "p1", ExitCountry: "DE"}}
		cfg.Subscriptions = []config.Subscription{
			{Name: "p1", URL: "https://p1.invalid/sub"},
			{Name: "p2", URL: "https://p2.invalid/sub"},
		}
	})

	// Start as a real daemon: `down` shuts it down gracefully, so the final
	// binding write completes before the state file disappears.
	code, stdout, stderr := daemonRun(t, home, "up", "--port", "0")
	if code != 0 {
		t.Fatalf("up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	listen, _ := daemonData(t, stdout)["listen"].(string)
	if listen == "" {
		t.Fatal("up returned no listen address")
	}

	// First site: may be provisional. Second site: its member is known by
	// now, so this binding is non-provisional and must be persisted.
	accountsClient(t, listen, "alice", pass, site1)
	bindingsRetryUntil(t, "the p1 upstream relaying the first site", func() bool {
		return up1.count(site1) >= 1
	})
	accountsClient(t, listen, "alice", pass, site2)
	bindingsRetryUntil(t, "the p1 upstream relaying the second site", func() bool {
		return up1.count(site2) >= 1
	})
	bindingsRetryUntil(t, "affinity.bindings >= 2 (both sites bound)", func() bool {
		st := bindingsStatusOf(t, home)
		return st.Data.Affinity != nil && st.Data.Affinity.Bindings >= 2
	})

	// Graceful stop: the shutdown writes the last snapshot.
	if code, stdout, stderr := daemonRun(t, home, "down"); code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}

	// While the daemon is down, make the p2 node the preferred one: a fresh
	// pick on the next start would choose it. The whole configuration is
	// written again (accounts and subscriptions), not a fresh default.
	cfg := loadConfigFile(t, home)
	cfg.Accounts[0].Provider = "p2,p1"
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatalf("save reordered config: %v", err)
	}

	code, stdout, stderr = daemonRun(t, home, "up", "--port", "0")
	if code != 0 {
		t.Fatalf("second up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	listen, _ = daemonData(t, stdout)["listen"].(string)
	if listen == "" {
		t.Fatal("second up returned no listen address")
	}

	// The core reports what it restored; restored counts only the
	// non-provisional bindings, so site2's binding gives at least 1.
	bindingsRetryUntil(t, "affinity.restored >= 1 on the restarted daemon", func() bool {
		st := bindingsStatusOf(t, home)
		return st.Data.Affinity != nil && st.Data.Affinity.Restored >= 1
	})

	// The same upstream must serve the second site again, and the other one
	// none of its connections (a fresh pick would prefer the p2 node).
	accountsClient(t, listen, "alice", pass, site2)
	bindingsRetryUntil(t, "the p1 upstream relaying the second site again", func() bool {
		return up1.count(site2) >= 2
	})
	if n := up2.count(site2); n != 0 {
		t.Errorf("the p2 upstream relayed %d connections of the bound site, want 0 (the stored binding must keep the p1 node)", n)
	}
	if code, stdout, stderr := daemonRun(t, home, "down"); code != 0 {
		t.Fatalf("final down exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
}
