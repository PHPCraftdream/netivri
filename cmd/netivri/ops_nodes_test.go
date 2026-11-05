package main

import (
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// nodesSeedConfig writes the config with the given subscriptions.
func nodesSeedConfig(t *testing.T, home string, subs ...config.Subscription) {
	t.Helper()
	cfg := config.Default()
	cfg.Subscriptions = subs
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
}

// nodesSeedSnapshot stores one snapshot directly through the store serializer.
func nodesSeedSnapshot(t *testing.T, home, name string, nodes []subscription.Node) {
	t.Helper()
	if err := subscription.NewStore(home+"/subscriptions").Save(name, subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
}

func TestNodeListSharedOrderAndFields(t *testing.T) {
	home := t.TempDir()
	nodesSeedConfig(t, home,
		config.Subscription{Name: "a", URL: "https://a.invalid/sub"},
		config.Subscription{Name: "b", URL: "https://b.invalid/sub"},
	)
	nodesSeedSnapshot(t, home, "a", []subscription.Node{
		{ID: "aa1", Name: "Frankfurt 1", Type: "ss", Server: "srv-a.example", Port: 8388, Country: "DE"},
		{ID: "aa2", Name: "Tokyo 01", Type: "vmess", Server: "srv-a.example", Port: 443, Country: "JP"},
	})
	nodesSeedSnapshot(t, home, "b", []subscription.Node{
		{ID: "bb1", Name: "Helsinki 1", Type: "ss", Server: "srv-b.example", Port: 8388, Country: "FI"},
	})
	entries, err := NodeList(home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	wantSubs := []string{"a", "a", "b"}
	wantIDs := []string{"aa1", "aa2", "bb1"}
	for i, e := range entries {
		if e.Sub != wantSubs[i] || e.ID != wantIDs[i] {
			t.Fatalf("entry %d = %+v, want %s/%s", i, e, wantSubs[i], wantIDs[i])
		}
		if e.Label() != wantSubs[i]+"/"+wantIDs[i] {
			t.Fatalf("entry %d label = %q", i, e.Label())
		}
	}
	if entries[0].Name != "Frankfurt 1" || entries[0].Type != "ss" || entries[0].Server != "srv-a.example" || entries[0].Port != 8388 || entries[0].Country != "DE" {
		t.Fatalf("entry 0 fields = %+v", entries[0])
	}
}

func TestNodeListSharedFilters(t *testing.T) {
	home := t.TempDir()
	nodesSeedConfig(t, home,
		config.Subscription{Name: "a", URL: "https://a.invalid/sub"},
		config.Subscription{Name: "b", URL: "https://b.invalid/sub"},
	)
	nodesSeedSnapshot(t, home, "a", []subscription.Node{
		{ID: "aa1", Country: "DE"},
		{ID: "aa2", Country: "JP"},
	})
	nodesSeedSnapshot(t, home, "b", []subscription.Node{
		{ID: "bb1", Country: "FI"},
	})
	if es, err := NodeList(home, "b", ""); err != nil || len(es) != 1 || es[0].ID != "bb1" {
		t.Fatalf("--sub b = %v, %v", es, err)
	}
	if _, err := NodeList(home, "nosuch", ""); err == nil {
		t.Fatal("unknown sub: want an error")
	} else {
		assertCode(t, err, codeSubNotFound, cli.ExitNotFound)
	}
	// Country filter, case-insensitive like the command's upper-casing.
	if es, err := NodeList(home, "", "fi"); err != nil || len(es) != 1 || es[0].Country != "FI" {
		t.Fatalf("country fi = %v, %v", es, err)
	}
	if es, err := NodeList(home, "a", "DE"); err != nil || len(es) != 1 || es[0].ID != "aa1" {
		t.Fatalf("country DE on a = %v, %v", es, err)
	}
	if es, err := NodeList(home, "a", "NL"); err != nil || len(es) != 0 {
		t.Fatalf("country NL on a = %v, %v", es, err)
	}
	// A subscription without a stored snapshot is skipped, not an error.
	nodesSeedConfig(t, home,
		config.Subscription{Name: "a", URL: "https://a.invalid/sub"},
		config.Subscription{Name: "fresh", URL: "https://fresh.invalid/sub"},
	)
	es, err := NodeList(home, "", "")
	if err != nil || len(es) != 2 {
		t.Fatalf("missing snapshot skipped = %v, %v", es, err)
	}
}
