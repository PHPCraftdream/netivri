package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// seedStats writes a small, exactly known data set into <home>/stats.db:
// main/a (DE) and alt/b (NL), all samples a minute old.
func seedStats(t *testing.T, home string) {
	t.Helper()
	store, err := stats.Open(stats.Options{Path: statsPath(home)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := store.SetNodes([]stats.NodeInfo{
		{Label: "main/a", Provider: "main", Country: "DE", Name: "Germany 1"},
		{Label: "alt/b", Provider: "alt", Country: "NL"},
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	for _, ms := range []int{100, 200, 300} {
		store.Record(stats.Sample{At: at, Label: "main/a", Kind: stats.KindProbe, OK: true, Delay: time.Duration(ms) * time.Millisecond})
	}
	store.Record(stats.Sample{At: at, Label: "main/a", Kind: stats.KindProbe, OK: false})
	for _, ms := range []int{50, 150} {
		store.Record(stats.Sample{At: at, Label: "main/a", Kind: stats.KindDial, OK: true, Delay: time.Duration(ms) * time.Millisecond})
	}
	store.Record(stats.Sample{At: at, Label: "main/a", Kind: stats.KindDial, OK: false})
	// 1 MiB in one second is 1024 KiB/s.
	store.Record(stats.Sample{At: at, Label: "main/a", Kind: stats.KindThroughput, OK: true, Delay: time.Second, Bytes: 1 << 20})
	store.Record(stats.Sample{At: at, Label: "alt/b", Kind: stats.KindProbe, OK: true, Delay: 50 * time.Millisecond})
	// Per-account samples: alice on main/a, bob on alt/b.
	for _, ok := range []bool{true, true, false} {
		store.RecordAccount(stats.AccountSample{At: at, Login: "alice", Label: "main/a", Kind: stats.KindDial, OK: ok, Delay: 100 * time.Millisecond})
	}
	store.RecordAccount(stats.AccountSample{At: at, Login: "alice", Label: "main/a", Kind: stats.KindThroughput, OK: true, Delay: 5 * time.Second, Bytes: 3 << 20, Up: 1 << 20, Down: 2 << 20})
	store.RecordAccount(stats.AccountSample{At: at, Login: "bob", Label: "alt/b", Kind: stats.KindDial, OK: true, Delay: 50 * time.Millisecond})
	if err := store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func statsRun(t *testing.T, home string, args ...string) (map[string]any, error) {
	t.Helper()
	var out bytes.Buffer
	err := runStats(append(args, "--home", home), &out)
	if err != nil {
		return nil, err
	}
	return decodeData(t, &out), nil
}

func statsItemsOf(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()
	raw, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("items = %#v", data["items"])
	}
	items := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		items = append(items, r.(map[string]any))
	}
	return items
}

func TestStatsUnavailableWithoutDatabase(t *testing.T) {
	home := t.TempDir()
	_, err := statsRun(t, home)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "STATS_UNAVAILABLE" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("unexpected error: %#v", err)
	}
	if _, statErr := os.Stat(statsPath(home)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stats must not create the database (stat err %v)", statErr)
	}
}

func TestStatsPerNodeValues(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	data, err := statsRun(t, home, "--since", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if data["since"] != "1h0m0s" || data["by"] != "node" {
		t.Fatalf("header = %v %v", data["since"], data["by"])
	}
	items := statsItemsOf(t, data)
	if len(items) != 2 || items[0]["label"] != "alt/b" || items[1]["label"] != "main/a" {
		t.Fatalf("items = %v", items)
	}
	a := items[1]
	if a["provider"] != "main" || a["country"] != "DE" || a["probes"] != float64(4) || a["uptime_pct"] != 75.0 {
		t.Fatalf("main/a = %v", a)
	}
	if a["name"] != "Germany 1" {
		t.Fatalf("main/a name = %v, want Germany 1", a["name"])
	}
	if _, has := items[0]["name"]; has {
		t.Fatalf("alt/b must not carry a name: %v", items[0])
	}
	delay := a["delay_ms"].(map[string]any)
	if delay["avg"] != float64(200) || delay["p50"] != float64(200) || delay["p95"] != float64(300) {
		t.Fatalf("delay_ms = %v", delay)
	}
	if a["dials"] != float64(3) || a["dial_ok_pct"] != 66.7 || a["dial_ms_avg"] != float64(100) {
		t.Fatalf("dials = %v %v %v", a["dials"], a["dial_ok_pct"], a["dial_ms_avg"])
	}
	tp := a["throughput_kbps"].(map[string]any)
	if tp["p50"] != float64(1024) || tp["max"] != float64(1024) || a["bytes"] != float64(1<<20) {
		t.Fatalf("throughput = %v bytes %v", tp, a["bytes"])
	}
	if a["last_seen"] == nil || a["last_ok"] == nil {
		t.Fatalf("last_seen/last_ok = %v %v", a["last_seen"], a["last_ok"])
	}
	// alt/b never dialed: percentages are null, not 0.
	b := items[0]
	if b["dials"] != float64(0) || b["dial_ok_pct"] != nil || b["uptime_pct"] != 100.0 {
		t.Fatalf("alt/b = %v", b)
	}
}

func TestStatsFilters(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"country", []string{"--country", "de"}, "main/a"},
		{"sub", []string{"--sub", "alt"}, "alt/b"},
		{"node", []string{"--node", "main/a"}, "main/a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := statsRun(t, home, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			items := statsItemsOf(t, data)
			if len(items) != 1 || items[0]["label"] != tc.want {
				t.Fatalf("items = %v, want only %s", items, tc.want)
			}
		})
	}
}

func TestStatsGroupedByCountryAndProvider(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	data, err := statsRun(t, home, "--by", "country")
	if err != nil {
		t.Fatal(err)
	}
	items := statsItemsOf(t, data)
	if len(items) != 2 || items[0]["country"] != "DE" || items[1]["country"] != "NL" {
		t.Fatalf("by country = %v", items)
	}
	if items[0]["nodes"] != float64(1) || items[0]["uptime_pct"] != 75.0 {
		t.Fatalf("DE = %v", items[0])
	}
	if _, has := items[0]["delay_ms"].(map[string]any)["p50"]; has {
		t.Fatal("grouped rows must not carry percentiles")
	}
	data, err = statsRun(t, home, "--by", "provider")
	if err != nil {
		t.Fatal(err)
	}
	items = statsItemsOf(t, data)
	if len(items) != 2 || items[0]["provider"] != "alt" || items[1]["provider"] != "main" {
		t.Fatalf("by provider = %v", items)
	}
}

func TestStatsUsageErrors(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	cases := map[string][]string{
		"bad since":      {"--since", "soon"},
		"zero since":     {"--since", "0s"},
		"too long":       {"--since", "400d"},
		"bad by":         {"--by", "city"},
		"bad country":    {"--country", "germany"},
		"stray argument": {"extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := statsRun(t, home, args...)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Exit != cli.ExitUsage {
				t.Fatalf("not a usage error: %#v", err)
			}
		})
	}
}

func TestParseWindowDays(t *testing.T) {
	got, err := parseWindow("7d")
	if err != nil || got != 7*24*time.Hour {
		t.Fatalf("7d = %v %v", got, err)
	}
	got, err = parseWindow("90m")
	if err != nil || got != 90*time.Minute {
		t.Fatalf("90m = %v %v", got, err)
	}
}

func TestStatsByAccount(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	data, err := statsRun(t, home, "--by", "account", "--since", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if data["by"] != "account" || data["since"] != "1h0m0s" {
		t.Fatalf("header = %v %v", data["since"], data["by"])
	}
	items := statsItemsOf(t, data)
	if len(items) != 2 || items[0]["login"] != "alice" || items[1]["login"] != "bob" {
		t.Fatalf("items = %v", items)
	}
	a := items[0]
	if a["bytes_up"] != float64(1<<20) || a["bytes_down"] != float64(2<<20) ||
		a["dials"] != float64(3) || a["dial_ok_pct"] != 66.7 || a["nodes"] != float64(1) {
		t.Fatalf("alice = %v", a)
	}
	if a["countries"] != "DE" || a["providers"] != "main" {
		t.Fatalf("alice countries/providers = %v %v", a["countries"], a["providers"])
	}
	b := items[1]
	if b["nodes"] != float64(1) || b["countries"] != "NL" {
		t.Fatalf("bob = %v", b)
	}
}

func TestStatsByAccountFilter(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	for _, login := range []string{"alice", "bob"} {
		data, err := statsRun(t, home, "--by", "account", "--account", login)
		if err != nil {
			t.Fatal(err)
		}
		items := statsItemsOf(t, data)
		if len(items) != 1 || items[0]["login"] != login {
			t.Fatalf("--account %s items = %v", login, items)
		}
	}
}

func TestStatsAccountUsageErrors(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	cases := map[string][]string{
		"bad since": {"--by", "account", "--since", "soon"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := statsRun(t, home, args...)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Exit != cli.ExitUsage {
				t.Fatalf("not a usage error: %#v", err)
			}
		})
	}
}

func TestStatsObserverRecordsAllKinds(t *testing.T) {
	home := t.TempDir()
	store, err := stats.Open(stats.Options{Path: statsPath(home)})
	if err != nil {
		t.Fatal(err)
	}
	obs := statsObserver{store: store}
	obs.ObserveProbe("p/n", true, 80*time.Millisecond)
	obs.ObserveDial("p/n", false, 5*time.Millisecond)
	obs.ObserveThroughput("p/n", 2048, 2*time.Second)
	if err := store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	nodes, err := store.Nodes(stats.Query{Since: time.Hour, Label: "p/n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %+v", nodes)
	}
	n := nodes[0]
	if n.Probes != 1 || n.ProbesOK != 1 || n.Dials != 1 || n.DialsOK != 0 || n.Bytes != 2048 {
		t.Fatalf("recorded %+v", n)
	}
	if n.ThroughputMaxKBps != 1 {
		t.Fatalf("throughput max = %d KiB/s, want 1 (2 KiB in 2 s)", n.ThroughputMaxKBps)
	}
}

func TestOpenStatsWithoutNodesCreatesNothing(t *testing.T) {
	home := t.TempDir()
	if store := openStats(home, nil, t.Logf); store != nil {
		t.Fatal("a store was opened without nodes")
	}
	if _, err := os.Stat(statsPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database created without nodes (stat err %v)", err)
	}
}

func TestOpenStatsFailureIsLoggedNotFatal(t *testing.T) {
	// A regular file where the directory should be makes the open fail.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	logged := 0
	store := openStats(blocker, []stats.NodeInfo{{Label: "p/n"}}, func(string, ...any) { logged++ })
	if store != nil {
		_ = store.Close()
		t.Fatal("open succeeded under a regular file")
	}
	if logged == 0 {
		t.Fatal("the failure was not logged")
	}
}

func TestStatsAccountFilters(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)

	// The node filters restrict the account rows to samples on matching nodes.
	data, err := statsRun(t, home, "--by", "account", "--country", "DE")
	if err != nil {
		t.Fatal(err)
	}
	items := statsItemsOf(t, data)
	if len(items) != 1 || items[0]["login"] != "alice" {
		t.Fatalf("--country DE account items = %v, want only alice", items)
	}

	data, err = statsRun(t, home, "--by", "account", "--node", "alt/b")
	if err != nil {
		t.Fatal(err)
	}
	items = statsItemsOf(t, data)
	if len(items) != 1 || items[0]["login"] != "bob" {
		t.Fatalf("--node alt/b account items = %v, want only bob", items)
	}

	// The account view of node grouping: a subset built from account samples.
	data, err = statsRun(t, home, "--by", "country", "--account", "alice")
	if err != nil {
		t.Fatal(err)
	}
	items = statsItemsOf(t, data)
	if len(items) != 1 {
		t.Fatalf("--by country --account alice items = %v, want one DE group", items)
	}
	g := items[0]
	if g["country"] != "DE" || g["dials"] != float64(3) || g["dial_ok_pct"] != 66.7 ||
		g["bytes"] != float64(3<<20) || g["nodes"] != float64(1) || g["probes"] != float64(0) ||
		g["uptime_pct"] != nil {
		t.Fatalf("DE group from account samples = %v", g)
	}

	data, err = statsRun(t, home, "--by", "node", "--account", "bob")
	if err != nil {
		t.Fatal(err)
	}
	items = statsItemsOf(t, data)
	if len(items) != 1 || items[0]["label"] != "alt/b" {
		t.Fatalf("--by node --account bob items = %v, want only alt/b", items)
	}
	n := items[0]
	if n["dials"] != float64(1) || n["dial_ok_pct"] != 100.0 ||
		n["bytes"] != float64(0) || n["probes"] != float64(0) {
		t.Fatalf("alt/b from bob's samples = %v", n)
	}
}
