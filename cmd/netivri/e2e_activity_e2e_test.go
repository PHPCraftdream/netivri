//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

type e2eActivity struct {
	Login         string   `json:"login"`
	Active        bool     `json:"active"`
	Connections   int      `json:"connections"`
	UploadBytes   int64    `json:"upload_bytes"`
	DownloadBytes int64    `json:"download_bytes"`
	Providers     []string `json:"providers"`
	Countries     []string `json:"countries"`
	Nodes         []struct {
		Node        string `json:"node"`
		Name        string `json:"name"`
		Provider    string `json:"provider"`
		Country     string `json:"country"`
		Connections int    `json:"connections"`
	} `json:"nodes"`
}

// e2eStatusActivity runs `status` against the angel of home.
func e2eStatusActivity(t *testing.T, home string) (int, map[string]e2eActivity) {
	t.Helper()
	code, stdout, stderr := angelRun(t, home, "status")
	if code != 0 {
		t.Fatalf("status exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	var env struct {
		Data struct {
			ActiveAccounts int           `json:"active_accounts"`
			Activity       []e2eActivity `json:"activity"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode status %q: %v", stdout, err)
	}
	byLogin := make(map[string]e2eActivity, len(env.Data.Activity))
	for _, a := range env.Data.Activity {
		byLogin[a.Login] = a
	}
	return env.Data.ActiveAccounts, byLogin
}

// e2eActivityState is the angel started by TestE2EStatusShowsWhoIsActive.
type e2eActivityState struct {
	home    string
	backend string
	up      *accountsUpstream
	listen  string
}

// TestE2EStatusShowsWhoIsActive runs an angel with a routed account (alice,
// DE, provider fixture) and a direct one (bob), opens connections and reads
// who is active, through which node, provider and country.
func TestE2EStatusShowsWhoIsActive(t *testing.T) {
	st := setupActivityAngel(t)
	checkActivityIdle(t, st.home)

	alice := socks5AuthConnect(t, st.listen, "alice", accountsDEPass, st.backend)
	defer func() { _ = alice.Close() }()
	echoRoundTrip(t, alice, alice)
	checkAliceConnected(t, st.home)

	bob := socks5AuthConnect(t, st.listen, "bob", accountsDEPass, st.backend)
	defer func() { _ = bob.Close() }()
	echoRoundTrip(t, bob, bob)
	checkBothConnected(t, st)
}

// setupActivityAngel writes the fixture config and snapshot and starts the angel.
func setupActivityAngel(t *testing.T) e2eActivityState {
	t.Helper()
	home := t.TempDir()
	hash, err := auth.Hash(accountsDEPass)
	if err != nil {
		t.Fatal(err)
	}
	st := e2eActivityState{home: home, backend: accountsEcho(t), up: accountsServer(t)}
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "fixture", ExitCountry: "DE"},
			{Login: "bob", Password: hash},
		}
		cfg.Subscriptions = []config.Subscription{{Name: "fixture", URL: "https://fixture.invalid/subscription"}}
	})
	host, portText, err := net.SplitHostPort(st.up.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)
	node := subscription.Node{ID: "de-node", Name: "DE Fixture 1", Type: "socks5", Server: host, Port: port, Country: "DE",
		Raw: map[string]any{"name": "DE Fixture 1", "type": "socks5", "server": host, "port": port}}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("fixture", subscription.Snapshot{FetchedAt: time.Now(), Nodes: []subscription.Node{node}}); err != nil {
		t.Fatal(err)
	}
	p := startUp(t, home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	st.listen = p.Status().Data.Listen
	return st
}

// checkActivityIdle checks that an idle angel reports no active account.
func checkActivityIdle(t *testing.T, home string) {
	t.Helper()
	active, who := e2eStatusActivity(t, home)
	if active != 0 || len(who) != 2 || who["alice"].Active || who["bob"].Active {
		t.Fatalf("idle angel: active=%d who=%+v", active, who)
	}
}

// checkAliceConnected checks alice's node, provider, country and traffic.
func checkAliceConnected(t *testing.T, home string) {
	t.Helper()
	active, who := e2eStatusActivity(t, home)
	a := who["alice"]
	if active != 1 || !a.Active || a.Connections != 1 || who["bob"].Active {
		t.Fatalf("alice connected: active=%d who=%+v", active, who)
	}
	if len(a.Nodes) != 1 || !strings.HasPrefix(a.Nodes[0].Node, "fixture/") ||
		a.Nodes[0].Name != "DE Fixture 1" ||
		a.Nodes[0].Provider != "fixture" || a.Nodes[0].Country != "DE" || a.Nodes[0].Connections != 1 {
		t.Errorf("alice nodes = %+v", a.Nodes)
	}
	if len(a.Providers) != 1 || a.Providers[0] != "fixture" || len(a.Countries) != 1 || a.Countries[0] != "DE" {
		t.Errorf("alice providers %v countries %v", a.Providers, a.Countries)
	}
	if a.UploadBytes <= 0 || a.DownloadBytes <= 0 {
		t.Errorf("alice moved no bytes: up %d down %d", a.UploadBytes, a.DownloadBytes)
	}
}

// checkBothConnected checks bob's direct route and that only alice reached the upstream.
func checkBothConnected(t *testing.T, st e2eActivityState) {
	t.Helper()
	active, who := e2eStatusActivity(t, st.home)
	b := who["bob"]
	if active != 2 || !who["alice"].Active || !b.Active || b.Connections != 1 {
		t.Fatalf("both connected: active=%d who=%+v", active, who)
	}
	if len(b.Providers) != 0 || len(b.Countries) != 0 {
		t.Errorf("bob has no route but reports providers %v countries %v", b.Providers, b.Countries)
	}
	if st.up.count.Load() != 1 {
		t.Errorf("the upstream saw %d connections, want only alice's 1", st.up.count.Load())
	}
}
