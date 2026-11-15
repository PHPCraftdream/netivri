//go:build e2e

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

type e2eShowTier struct {
	Tier      int      `json:"tier"`
	Country   string   `json:"country"`
	Skipped   bool     `json:"skipped"`
	Reason    string   `json:"reason"`
	Providers []string `json:"providers"`
	Nodes     []struct {
		Label       string `json:"label"`
		Provider    string `json:"provider"`
		Country     string `json:"country"`
		Tier        int    `json:"tier"`
		Alive       *bool  `json:"alive"`
		DelayMS     *int64 `json:"delay_ms"`
		Connections *int   `json:"connections"`
	} `json:"nodes"`
}

// e2eShowEnv is the `account show` JSON envelope.
type e2eShowEnv struct {
	Data e2eShowData `json:"data"`
}

type e2eShowData struct {
	Login     string        `json:"login"`
	Providers []string      `json:"providers"`
	Countries []string      `json:"countries"`
	Tiers     []e2eShowTier `json:"tiers"`
	Live      bool          `json:"live"`
}

// e2eShowState carries one account-show lifecycle across its phases.
type e2eShowState struct {
	home    string
	backend string
	hash    string
	env     e2eShowEnv
	code    int
	out     string
	stderr  string
}

// TestE2EAccountShow runs `account show` without and with a running angel:
// first the static route with skipped tiers, then the live probe and traffic
// state of the same nodes.
func TestE2EAccountShow(t *testing.T) {
	st := setupAccountShow(t)
	checkAccountShowStatic(t, st)
	checkAccountShowErrors(t, st)
	checkAccountShowLive(t, st)
}

func setupAccountShow(t *testing.T) *e2eShowState {
	t.Helper()
	st := &e2eShowState{home: t.TempDir()}
	st.backend = accountsEcho(t)
	up1 := accountsServer(t)
	up2 := accountsServer(t)
	hash, err := auth.Hash(accountsDEPass)
	if err != nil {
		t.Fatal(err)
	}
	st.hash = hash
	writeConfig(t, st.home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "p1,p2", ExitCountry: "JP,DE"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "p1", URL: "https://p1.invalid/sub"},
			{Name: "p2", URL: "https://p2.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, st.home, "p1", []subscription.Node{accountsSocksNode("de-p1", "DE", up1)})
	accountsSaveSnapshot(t, st.home, "p2", []subscription.Node{accountsSocksNode("de-p2", "DE", up2)})
	return st
}

// checkAccountShowStatic checks the static route: skipped tiers and live
// fields as they stand without an angel.
func checkAccountShowStatic(t *testing.T, st *e2eShowState) {
	t.Helper()
	// Without an angel the static route stands, live fields stay null.
	// JP has no node: both JP pairs are skipped and do not consume a tier
	// index, so DE/p1 is tier 0 and DE/p2 is tier 1.
	st.code, st.out, st.stderr = accountsCommand(t, st.home, "show", "alice")
	if st.code != 0 {
		t.Fatalf("show: exit %d (stderr %s)", st.code, st.stderr)
	}
	assertNoSecrets(t, st.out, st.stderr, accountsDEPass, st.hash)
	if err := json.Unmarshal([]byte(st.out), &st.env); err != nil {
		t.Fatalf("decode show %s: %v", st.out, err)
	}
	d := st.env.Data
	if d.Login != "alice" || d.Live ||
		len(d.Providers) != 2 || d.Providers[0] != "p1" || d.Providers[1] != "p2" ||
		len(d.Countries) != 2 || d.Countries[0] != "JP" || d.Countries[1] != "DE" {
		t.Fatalf("show data = %+v", d)
	}
	if len(d.Tiers) != 4 {
		t.Fatalf("tiers = %+v", d.Tiers)
	}
	checkShowTier(t, d.Tiers, 0, "JP", []string{"p1"}, true, 0)
	checkShowTier(t, d.Tiers, 1, "JP", []string{"p2"}, true, 0)
	checkShowTier(t, d.Tiers, 2, "DE", []string{"p1"}, false, 0)
	checkShowTier(t, d.Tiers, 3, "DE", []string{"p2"}, false, 1)
	if d.Tiers[2].Nodes[0].Label != "p1/de-p1" {
		t.Errorf("DE/p1 node = %+v", d.Tiers[2].Nodes)
	}
	if d.Tiers[3].Nodes[0].Label != "p2/de-p2" {
		t.Errorf("DE/p2 node = %+v", d.Tiers[3].Nodes)
	}
	checkShowNoLiveData(t, d.Tiers)
}

func checkShowTier(t *testing.T, tiers []e2eShowTier, i int, cc string, prov []string, skip bool, tier int) {
	t.Helper()
	got := tiers[i]
	if got.Country != cc || len(got.Providers) != 1 || got.Providers[0] != prov[0] || got.Skipped != skip || got.Tier != tier {
		t.Errorf("tier %d = %+v, want country %s providers %s skipped %v index %d", i, got, cc, prov, skip, tier)
	}
}

func checkShowNoLiveData(t *testing.T, tiers []e2eShowTier) {
	t.Helper()
	for i, tr := range tiers {
		for _, n := range tr.Nodes {
			if n.Alive != nil || n.DelayMS != nil || n.Connections != nil {
				t.Errorf("tier %d node %+v has live data without an angel", i, n)
			}
		}
	}
}

// checkAccountShowErrors checks the errors: unknown account (exit 5) and an
// invalid login (exit 2).
func checkAccountShowErrors(t *testing.T, st *e2eShowState) {
	t.Helper()
	st.code, st.out, st.stderr = accountsCommand(t, st.home, "show", "ghost")
	if st.code != 5 || decodeErrEnvelope(t, st.stderr).Error.Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("show ghost: exit %d, stderr %s", st.code, st.stderr)
	}
	assertNoSecrets(t, st.out, st.stderr, accountsDEPass, st.hash)
	st.code, st.out, st.stderr = accountsCommand(t, st.home, "show", "bad!login")
	if st.code != 2 || decodeErrEnvelope(t, st.stderr).Error.Code != "ACCOUNT_INVALID" {
		t.Fatalf("show bad!login: exit %d, stderr %s", st.code, st.stderr)
	}
	assertNoSecrets(t, st.out, st.stderr, accountsDEPass, st.hash)
}

// checkAccountShowLive checks the same command with a running angel: the live
// state, then the probe data once every node has a health record.
func checkAccountShowLive(t *testing.T, st *e2eShowState) {
	t.Helper()
	p := startUp(t, st.home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	listen := p.Status().Data.Listen
	alice := socks5AuthConnect(t, listen, "alice", accountsDEPass, st.backend)
	defer func() { _ = alice.Close() }()
	echoRoundTrip(t, alice, alice)

	// First read: structure and connection counts only. Probes run
	// asynchronously, so alive/delay may lag one exchange behind.
	runLiveShow(t, st, "live show")
	checkLiveConnections(t, st)

	// The angel probes asynchronously: wait until every node has a health
	// record.
	deadline := time.Now().Add(5 * time.Second)
	for !allNodesProbed(st.env.Data.Tiers) {
		if !time.Now().Before(deadline) {
			t.Fatalf("nodes still miss probe data after 5s: %s", st.out)
		}
		time.Sleep(100 * time.Millisecond)
		runLiveShow(t, st, "live show retry")
	}
	p.Stop()
	assertNoSecrets(t, p.Stdout(), p.Stderr(), accountsDEPass, st.hash)
}

// runLiveShow runs `show alice` against the live angel and decodes the
// envelope into st.env.
func runLiveShow(t *testing.T, st *e2eShowState, label string) {
	t.Helper()
	st.code, st.out, st.stderr = accountsCommand(t, st.home, "show", "alice")
	if st.code != 0 {
		t.Fatalf("%s: exit %d (stderr %s)", label, st.code, st.stderr)
	}
	assertNoSecrets(t, st.out, st.stderr, accountsDEPass, st.hash)
	if err := json.Unmarshal([]byte(st.out), &st.env); err != nil {
		t.Fatalf("decode live show %s: %v", st.out, err)
	}
	if !st.env.Data.Live {
		t.Fatalf("live = false: %s", st.out)
	}
}

// checkLiveConnections checks that exactly one node, in DE, has one connection.
func checkLiveConnections(t *testing.T, st *e2eShowState) {
	t.Helper()
	var connected, inDE int
	for _, tr := range st.env.Data.Tiers {
		for _, n := range tr.Nodes {
			if n.Connections != nil && *n.Connections == 1 {
				connected++
				if tr.Country == "DE" {
					inDE++
				}
			}
		}
	}
	if connected != 1 || inDE != 1 {
		t.Errorf("nodes with 1 connection = %d (in DE %d), want 1 each: %s", connected, inDE, st.out)
	}
}

// allNodesProbed reports whether every node has alive and delay data.
func allNodesProbed(tiers []e2eShowTier) bool {
	for _, tr := range tiers {
		for _, n := range tr.Nodes {
			if n.Alive == nil || n.DelayMS == nil {
				return false
			}
		}
	}
	return true
}
