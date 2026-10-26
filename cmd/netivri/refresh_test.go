package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestRefreshSchedule(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	hour := time.Hour
	fetched := map[string]time.Time{}
	at := func(name string) (time.Time, bool) {
		v, ok := fetched[name]
		return v, ok
	}
	has := func(got []string, want ...string) bool {
		return strings.Join(got, ",") == strings.Join(want, ",")
	}

	t.Run("nothing is due before the start delay", func(t *testing.T) {
		s := newRefreshSchedule(t0.Add(30 * time.Second))
		fetched = map[string]time.Time{"stale": t0.Add(-37 * hour)} // no snapshot for "new"
		if got := s.pick(t0, []string{"stale", "new"}, hour, at); len(got) != 0 {
			t.Fatalf("due at the start = %v, want none", got)
		}
		if got := s.pick(t0.Add(29*time.Second), []string{"stale", "new"}, hour, at); len(got) != 0 {
			t.Fatalf("due before the delay = %v, want none", got)
		}
		if got := s.pick(t0.Add(30*time.Second), []string{"stale", "new"}, hour, at); !has(got, "stale", "new") {
			t.Fatalf("due after the delay = %v, want stale and new", got)
		}
	})

	t.Run("a fresh snapshot waits for its interval", func(t *testing.T) {
		s := newRefreshSchedule(t0.Add(30 * time.Second))
		fetched = map[string]time.Time{"fresh": t0.Add(-20 * time.Minute)}
		if got := s.pick(t0.Add(time.Minute), []string{"fresh"}, hour, at); len(got) != 0 {
			t.Fatalf("due = %v, want none", got)
		}
		if got := s.pick(t0.Add(39*time.Minute), []string{"fresh"}, hour, at); len(got) != 0 {
			t.Fatalf("due after 59 minutes of age = %v, want none", got)
		}
		if got := s.pick(t0.Add(40*time.Minute), []string{"fresh"}, hour, at); !has(got, "fresh") {
			t.Fatalf("due after an hour of age = %v, want fresh", got)
		}
	})

	t.Run("success waits an interval, failure retries sooner", func(t *testing.T) {
		s := newRefreshSchedule(t0)
		fetched = map[string]time.Time{}
		s.pick(t0, []string{"a", "b"}, hour, at)
		s.done("a", t0, true, hour)
		s.done("b", t0, false, hour)
		if got := s.pick(t0.Add(9*time.Minute), []string{"a", "b"}, hour, at); len(got) != 0 {
			t.Fatalf("due after 9 minutes = %v, want none", got)
		}
		if got := s.pick(t0.Add(10*time.Minute), []string{"a", "b"}, hour, at); !has(got, "b") {
			t.Fatalf("due after the retry wait = %v, want only b", got)
		}
		if got := s.pick(t0.Add(hour), []string{"a", "b"}, hour, at); !has(got, "a", "b") {
			t.Fatalf("due after an hour = %v, want a and b", got)
		}
	})

	t.Run("a retry never waits longer than the interval", func(t *testing.T) {
		s := newRefreshSchedule(t0)
		s.done("a", t0, false, 5*time.Minute)
		if got := s.pick(t0.Add(5*time.Minute), []string{"a"}, 5*time.Minute, at); !has(got, "a") {
			t.Fatalf("due = %v, want a", got)
		}
	})

	t.Run("a shorter interval applies at once and removed names are forgotten", func(t *testing.T) {
		s := newRefreshSchedule(t0)
		s.done("a", t0, true, 24*time.Hour)
		s.done("gone", t0, true, hour)
		if got := s.pick(t0.Add(10*time.Minute), []string{"a"}, 5*time.Minute, at); len(got) != 0 {
			t.Fatalf("due 10 minutes after the shortening to 5m = %v, want none yet", got)
		}
		if got := s.pick(t0.Add(15*time.Minute), []string{"a"}, 5*time.Minute, at); !has(got, "a") {
			t.Fatalf("due after the shortened interval = %v, want a (not in 24 hours)", got)
		}
		if _, kept := s.due["gone"]; kept {
			t.Error("a subscription removed from the config is still scheduled")
		}
	})
}

func TestRefreshInfoInterval(t *testing.T) {
	l := newLiveAngel(upOptions{}, func(string, ...any) {}, func() {})
	l.mu.Lock()
	defer l.mu.Unlock()
	cases := map[string]int64{"": 3600, "30m": 1800, "168h": 604800, config.UpdateOff: 0}
	for raw, want := range cases {
		cfg := config.Default()
		cfg.Fetch.UpdateInterval = raw
		l.latest = cfg
		if got := l.refreshInfoLocked().IntervalSeconds; got != want {
			t.Errorf("interval %q reported as %d seconds, want %d", raw, got, want)
		}
	}
}

// A refreshed list that would leave an account without nodes must not replace
// the working one; a refreshed list that keeps every route is stored.
func TestRefreshOneKeepsWorkingRoutes(t *testing.T) {
	home := t.TempDir()
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("main", subscription.Snapshot{FetchedAt: time.Now().Add(-37 * time.Hour), Nodes: []subscription.Node{node("us-1", "US")}}); err != nil {
		t.Fatal(err)
	}
	old := subFetch
	subFetch = fetchMap(t, map[string]any{"https://main.invalid/sub": testBodyFI}, nil)
	t.Cleanup(func() { subFetch = old })

	var logs []string
	l := newLiveAngel(upOptions{Home: home}, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }, func() {})
	sub := config.Subscription{Name: "main", URL: "https://main.invalid/sub"}
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{sub}

	// alice exits through the US and the new list has only a Finnish node.
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "main", ExitCountry: "US"}}
	if l.refreshOne(t.Context(), store, cfg, sub) {
		t.Fatal("a list without the account's country was accepted")
	}
	got, err := store.Load("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].ID != "us-1" {
		t.Fatalf("the working snapshot was replaced: %+v", got.Nodes)
	}
	l.mu.Lock()
	failed, text := l.refreshFail, l.refreshErr
	l.mu.Unlock()
	if failed != 1 || !strings.Contains(text, "NO_NODES_FOR_COUNTRY") {
		t.Errorf("refresh failures = %d, error = %q", failed, text)
	}

	// bob exits through Finland: the new list keeps his route and is stored.
	cfg.Accounts = []config.Account{{Login: "bob", Password: config.InitPlaceholder, Provider: "main", ExitCountry: "FI"}}
	if !l.refreshOne(t.Context(), store, cfg, sub) {
		t.Fatal("a list that keeps every route was rejected")
	}
	got, err = store.Load("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Country != "FI" || time.Since(got.FetchedAt) > time.Minute {
		t.Fatalf("the refreshed snapshot was not stored: %+v fetched %v", got.Nodes, got.FetchedAt)
	}
	l.mu.Lock()
	okCount, text := l.refreshOK, l.refreshErr
	l.mu.Unlock()
	if okCount != 1 || text != "" {
		t.Errorf("refresh ok = %d, error = %q", okCount, text)
	}
	for _, line := range logs {
		if strings.Contains(line, "main.invalid") {
			t.Errorf("the log mentions the subscription URL: %q", line)
		}
	}
}

func TestAccountRoutePoolWithOverride(t *testing.T) {
	home := t.TempDir()
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("main", subscription.Snapshot{Nodes: []subscription.Node{node("old", "DE")}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://main.invalid/sub"}}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "main", ExitCountry: "DE"}}

	pool, err := accountRoutePool(home, cfg)
	if err != nil || pool.nodes[credentialRouteKey("main", "DE", "", "")][0]["name"] != "old" {
		t.Fatalf("stored pool = %+v, %v", pool.nodes, err)
	}
	over := map[string]*subscription.Snapshot{"main": {Nodes: []subscription.Node{node("new", "DE")}}}
	pool, err = accountRoutePoolWith(home, cfg, over)
	if err != nil || pool.nodes[credentialRouteKey("main", "DE", "", "")][0]["name"] != "new" {
		t.Fatalf("overridden pool = %+v, %v", pool.nodes, err)
	}
	over["main"] = &subscription.Snapshot{Nodes: []subscription.Node{node("other", "NL")}}
	if _, err := accountRoutePoolWith(home, cfg, over); err == nil {
		t.Fatal("a snapshot without the route's country built a pool")
	}
	if snap, err := store.Load("main"); err != nil || snap.Nodes[0].ID != "old" {
		t.Fatalf("the override touched the store: %+v, %v", snap, err)
	}
}
