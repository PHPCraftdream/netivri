package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestBuildAccountCountryPoolOrderingAndCountryFilter(t *testing.T) {
	accounts := []config.Account{{Login: "a", ExitCountry: "DE"}, {Login: "b", ExitCountry: "NL"}}
	snapshots := []*subscription.Snapshot{{Nodes: []subscription.Node{
		{ID: "z", Country: "DE", Raw: map[string]any{"name": "z"}},
		{ID: "a", Country: "DE", Raw: map[string]any{"name": "a"}},
		{ID: "b", Country: "NL", Raw: map[string]any{"name": "b"}},
		{ID: "x", Country: "US", Raw: map[string]any{"name": "x"}},
	}}}
	got, err := buildAccountCountryPool(accounts, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if got["DE"][0]["name"] != "a" || got["DE"][1]["name"] != "z" || len(got) != 2 {
		t.Fatalf("unexpected pool: %#v", got)
	}
}

func TestBuildAccountCountryPoolNoCountryAndEmptyAccounts(t *testing.T) {
	for _, accounts := range [][]config.Account{nil, {{Login: "default"}}} {
		got, err := buildAccountCountryPool(accounts, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("pool=%v err=%v", got, err)
		}
	}
}

func TestBuildAccountCountryPoolEmptyRequestedCountry(t *testing.T) {
	_, err := buildAccountCountryPool([]config.Account{{Login: "a", ExitCountry: "DE"}}, nil)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound || ce.Hint != "run netivri sub update before netivri up" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestAccountCountryPoolLoadsStoredSnapshotsAndSkipsMissing(t *testing.T) {
	home := t.TempDir()
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("matching", subscription.Snapshot{Nodes: []subscription.Node{{ID: "1", Country: "DE", Raw: map[string]any{"name": "fixture"}}}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Accounts: []config.Account{{Login: "a", ExitCountry: "DE"}}, Subscriptions: []config.Subscription{{Name: "missing"}, {Name: "matching"}}}
	got, routes, err := accountCountryPool(home, cfg)
	if err != nil || got["DE"][0]["name"] != "fixture" || routes["a"] != "DE" {
		t.Fatalf("pool=%v routes=%v err=%v", got, routes, err)
	}
}

func TestAccountCountryPoolMissingAllSnapshots(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "a", ExitCountry: "DE"}}, Subscriptions: []config.Subscription{{Name: "missing"}}}
	_, _, err := accountCountryPool(t.TempDir(), cfg)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestAccountStatusLiveRoutesAndNoCountry(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "z", ExitCountry: "DE"}, {Login: "a"}}}
	got := accountStatus(cfg, map[string]string{"z": "NL"})
	want := []struct{ Login, ExitCountry string }{{"a", ""}, {"z", "NL"}}
	if len(got) != len(want) {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i].Login != want[i].Login || got[i].ExitCountry != want[i].ExitCountry {
			t.Fatalf("got %#v", got)
		}
	}
}
