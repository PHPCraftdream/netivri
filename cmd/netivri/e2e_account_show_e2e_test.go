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

// TestE2EAccountShow runs `account show` without and with a running daemon:
// first the static route with skipped tiers, then the live probe and traffic
// state of the same nodes.
func TestE2EAccountShow(t *testing.T) {
	home := t.TempDir()
	backend := accountsEcho(t)
	up1 := accountsServer(t)
	up2 := accountsServer(t)
	hash, err := auth.Hash(accountsDEPass)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "p1,p2", ExitCountry: "JP,DE"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "p1", URL: "https://p1.invalid/sub"},
			{Name: "p2", URL: "https://p2.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, home, "p1", []subscription.Node{accountsSocksNode("de-p1", "DE", up1)})
	accountsSaveSnapshot(t, home, "p2", []subscription.Node{accountsSocksNode("de-p2", "DE", up2)})

	// Without a daemon the static route stands, live fields stay null.
	// JP has no node: both JP pairs are skipped and do not consume a tier
	// index, so DE/p1 is tier 0 and DE/p2 is tier 1.
	code, out, stderr := accountsCommand(t, home, "show", "alice")
	if code != 0 {
		t.Fatalf("show: exit %d (stderr %s)", code, stderr)
	}
	assertNoSecrets(t, out, stderr, accountsDEPass, hash)
	var env struct {
		Data struct {
			Login     string        `json:"login"`
			Providers []string      `json:"providers"`
			Countries []string      `json:"countries"`
			Tiers     []e2eShowTier `json:"tiers"`
			Live      bool          `json:"live"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode show %s: %v", out, err)
	}
	d := env.Data
	if d.Login != "alice" || d.Live ||
		len(d.Providers) != 2 || d.Providers[0] != "p1" || d.Providers[1] != "p2" ||
		len(d.Countries) != 2 || d.Countries[0] != "JP" || d.Countries[1] != "DE" {
		t.Fatalf("show data = %+v", d)
	}
	if len(d.Tiers) != 4 {
		t.Fatalf("tiers = %+v", d.Tiers)
	}
	checkTier := func(i int, cc string, prov []string, skip bool, tier int) {
		got := d.Tiers[i]
		if got.Country != cc || len(got.Providers) != 1 || got.Providers[0] != prov[0] || got.Skipped != skip || got.Tier != tier {
			t.Errorf("tier %d = %+v, want country %s providers %s skipped %v index %d", i, got, cc, prov, skip, tier)
		}
	}
	checkTier(0, "JP", []string{"p1"}, true, 0)
	checkTier(1, "JP", []string{"p2"}, true, 0)
	checkTier(2, "DE", []string{"p1"}, false, 0)
	checkTier(3, "DE", []string{"p2"}, false, 1)
	if d.Tiers[2].Nodes[0].Label != "p1/de-p1" {
		t.Errorf("DE/p1 node = %+v", d.Tiers[2].Nodes)
	}
	if d.Tiers[3].Nodes[0].Label != "p2/de-p2" {
		t.Errorf("DE/p2 node = %+v", d.Tiers[3].Nodes)
	}
	for i, tr := range d.Tiers {
		for _, n := range tr.Nodes {
			if n.Alive != nil || n.DelayMS != nil || n.Connections != nil {
				t.Errorf("tier %d node %+v has live data without a daemon", i, n)
			}
		}
	}

	// Errors: unknown account (exit 5) and an invalid login (exit 2).
	code, out, stderr = accountsCommand(t, home, "show", "ghost")
	if code != 5 || decodeErrEnvelope(t, stderr).Error.Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("show ghost: exit %d, stderr %s", code, stderr)
	}
	assertNoSecrets(t, out, stderr, accountsDEPass, hash)
	code, out, stderr = accountsCommand(t, home, "show", "bad!login")
	if code != 2 || decodeErrEnvelope(t, stderr).Error.Code != "ACCOUNT_INVALID" {
		t.Fatalf("show bad!login: exit %d, stderr %s", code, stderr)
	}
	assertNoSecrets(t, out, stderr, accountsDEPass, hash)

	// With a running daemon the same command reports the live state.
	p := startUp(t, home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	listen := p.Status().Data.Listen
	alice := socks5AuthConnect(t, listen, "alice", accountsDEPass, backend)
	defer alice.Close()
	echoRoundTrip(t, alice, alice)

	// First read: structure and connection counts only. Probes run
	// asynchronously, so alive/delay may lag one exchange behind.
	code, out, stderr = accountsCommand(t, home, "show", "alice")
	if code != 0 {
		t.Fatalf("live show: exit %d (stderr %s)", code, stderr)
	}
	assertNoSecrets(t, out, stderr, accountsDEPass, hash)
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode live show %s: %v", out, err)
	}
	if !env.Data.Live {
		t.Fatalf("live = false: %s", out)
	}
	var connected, inDE int
	for _, tr := range env.Data.Tiers {
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
		t.Errorf("nodes with 1 connection = %d (in DE %d), want 1 each: %s", connected, inDE, out)
	}

	// The daemon probes asynchronously: wait until every node has a health
	// record.
	deadline := time.Now().Add(5 * time.Second)
	for {
		probed := true
		for _, tr := range env.Data.Tiers {
			for _, n := range tr.Nodes {
				if n.Alive == nil || n.DelayMS == nil {
					probed = false
				}
			}
		}
		if probed {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("nodes still miss probe data after 5s: %s", out)
		}
		time.Sleep(100 * time.Millisecond)
		code, out, stderr = accountsCommand(t, home, "show", "alice")
		if code != 0 {
			t.Fatalf("live show retry: exit %d (stderr %s)", code, stderr)
		}
		assertNoSecrets(t, out, stderr, accountsDEPass, hash)
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode live show %s: %v", out, err)
		}
		if !env.Data.Live {
			t.Fatalf("live = false: %s", out)
		}
	}
	p.Stop()
	assertNoSecrets(t, p.Stdout(), p.Stderr(), accountsDEPass, hash)
}
