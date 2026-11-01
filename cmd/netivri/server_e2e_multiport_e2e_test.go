//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestE2EMultiportTrojanAnyTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const alicePass, bobPass = "multiport-e2e-alice-password", "multiport-e2e-bob-password"
	secrets := []string{alicePass, bobPass}
	backend := accountsEcho(t)
	aliceUp, bobUp := accountsServer(t), accountsServer(t)
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob password")
	}
	secrets = append(secrets, aliceHash, bobHash)
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "alice-route"}, {Login: "bob", Password: bobHash, Provider: "bob-route"}}
		cfg.Subscriptions = []config.Subscription{{Name: "alice-route", URL: "http://alice.invalid/sub"}, {Name: "bob-route", URL: "http://bob.invalid/sub"}}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", bobUp)})
	serverA := startUp(t, homeA, "--port", "0")
	defer serverA.Stop()
	port := freePort(t)
	for i, args := range [][]string{
		{"--protocol", "trojan", "--name", "trojan-edge", "--account", "alice", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "sni=trojan.local"},
		{"--protocol", "anytls", "--name", "anytls-edge", "--account", "bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "sni=anytls.local"},
	} {
		cmd := append([]string{"add"}, args...)
		code, out, stderr := serverRun(t, homeA, cmd...)
		if code != 0 {
			t.Fatalf("server add failed: %s", serverErrorSummary(out, stderr))
		}
		waitStatusReload(t, ctx, homeA, uint64(i+1))
	}
	links := map[string]string{"alice": e2eMultiportLink(t, homeA, "trojan-edge", "alice"), "bob": e2eMultiportLink(t, homeA, "anytls-edge", "bob")}
	secrets = append(secrets, links["alice"], links["bob"])
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		account := account
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, links[account]+"\n") })
	}
	subServer := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for local subscriptions")
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = subServer.Close()
		select {
		case <-done:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(done); _ = subServer.Serve(ln) }()
	homeB := t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-edge"
		code, out, stderr := accountsRun(t, homeB, "sub", "add", "http://"+ln.Addr().String()+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		code, out, stderr = accountsRun(t, homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
	}
	clientB := startUp(t, homeB, "--port", "0")
	defer func() { clientB.Stop() }()
	drive := func(login, pass string, own, other *accountsUpstream, retry bool) {
		t.Helper()
		beforeOwn, beforeOther := own.count.Load(), other.count.Load()
		deadline := time.Now().Add(30 * time.Second)
		var last error
		for {
			attemptCtx := ctx
			var stop context.CancelFunc = func() {}
			if retry {
				attemptCtx, stop = context.WithDeadline(ctx, deadline)
			}
			err := e2eAnyTLSClientEcho(attemptCtx, clientB.Status().Data.Listen, login, pass, backend)
			stop()
			if err == nil && own.count.Load() > beforeOwn && other.count.Load() == beforeOther {
				return
			}
			if err != nil {
				last = err
			} else {
				last = fmt.Errorf("account routing mismatch")
			}
			if !retry || !time.Now().Before(deadline) || ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
				break
			case <-time.After(150 * time.Millisecond):
			}
			if ctx.Err() != nil {
				break
			}
		}
		t.Logf("proxy echo failed: %v; %s", last, e2eAnyTLSDiagnostics(t, homeA, homeB, secrets...))
		t.Fatal("proxy echo or account routing failed")
	}
	drive("alice", alicePass, aliceUp, bobUp, true)
	drive("bob", bobPass, bobUp, aliceUp, true)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "trojan", "--name", "ambiguous", "--account", "alice", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--param", "sni=trojan.local")
	assertNoSecrets(t, out, stderr, secrets...)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(stderr), &envelope)
	if code != 6 || envelope.Error.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("ambiguous add returned unexpected result: %s", serverErrorSummary(out, stderr))
	}
	if code, out, stderr = serverRun(t, homeA, "remove", "trojan-edge"); code != 0 {
		t.Fatalf("remove Trojan: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 3)
	beforeAlice, beforeBob := aliceUp.count.Load(), bobUp.count.Load()
	if !serverA.Status().OK {
		t.Fatal("A did not remain running after server removal")
	}
	// B may have cached the removed endpoint; restart it before checking the surviving route.
	if code, out, stderr = accountsRun(t, homeB, "down"); code != 0 {
		t.Fatalf("B down: %s", serverErrorSummary(out, stderr))
	}
	clientB.Stop()
	clientB = startUp(t, homeB, "--port", "0")
	drive("bob", bobPass, bobUp, aliceUp, true)
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() <= beforeBob {
		t.Fatal("remaining Bob route reached the wrong account")
	}
}

func e2eMultiportLink(t *testing.T, home, name, login string) string {
	t.Helper()
	code, out, stderr := serverRun(t, home, "link", name, "--account", login, "--format", "uri")
	if code != 0 {
		t.Fatalf("server link failed: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || strings.TrimSpace(env.Data.Link) == "" {
		t.Fatal("invalid server link response")
	}
	return env.Data.Link
}
