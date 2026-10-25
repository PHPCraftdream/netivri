package main

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

func TestActivityInfoMapsNodesToProvidersAndCountries(t *testing.T) {
	accounts := []angel.AccountInfo{{Login: "alice"}, {Login: "bob"}, {Login: "carol"}}
	infos := []stats.NodeInfo{
		{Label: "main/a", Provider: "main", Country: "DE", Name: "Germany 1"},
		{Label: "main/b", Provider: "main", Country: "NL"},
		{Label: "alt/c", Provider: "alt", Country: "DE"},
	}
	live := []core.AccountActivity{
		{
			Login: "alice", Connections: 6, Upload: 60, Download: 600,
			Nodes: []core.NodeActivity{
				{Label: "main/a", Connections: 3, Upload: 30, Download: 300},
				{Label: "alt/c", Connections: 2, Upload: 20, Download: 200},
				{Label: "main/b", Connections: 1, Upload: 10, Download: 100},
			},
		},
		{
			Login: "carol", Connections: 1, Upload: 1, Download: 2,
			Nodes: []core.NodeActivity{{Label: core.ActivityDirect, Connections: 1, Upload: 1, Download: 2}},
		},
		{Login: "stranger", Connections: 9, Nodes: []core.NodeActivity{{Label: "main/a", Connections: 9}}},
	}
	got := activityInfo(accounts, live, infos)
	want := []angel.AccountActivity{
		{
			Login: "alice", Active: true, Connections: 6, UploadBytes: 60, DownloadBytes: 600,
			// Busiest first, each once: main (3 + 1 connections) is met first, then alt.
			Providers: []string{"main", "alt"},
			Countries: []string{"DE", "NL"},
			Nodes: []angel.NodeUse{
				{Node: "main/a", Name: "Germany 1", Provider: "main", Country: "DE", Connections: 3, UploadBytes: 30, DownloadBytes: 300},
				{Node: "alt/c", Provider: "alt", Country: "DE", Connections: 2, UploadBytes: 20, DownloadBytes: 200},
				{Node: "main/b", Provider: "main", Country: "NL", Connections: 1, UploadBytes: 10, DownloadBytes: 100},
			},
		},
		{Login: "bob", Providers: []string{}, Countries: []string{}, Nodes: []angel.NodeUse{}},
		{
			Login: "carol", Active: true, Connections: 1, UploadBytes: 1, DownloadBytes: 2,
			Providers: []string{}, Countries: []string{},
			Nodes: []angel.NodeUse{{Node: "direct", Connections: 1, UploadBytes: 1, DownloadBytes: 2}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("activity =\n%+v\nwant\n%+v", got, want)
	}
}

func TestActivityInfoWithoutAccounts(t *testing.T) {
	if got := activityInfo(nil, nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("activity = %#v, want an empty list", got)
	}
}

func TestLiveStatusReportsActivity(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	f.l.mu.Lock()
	infos := append([]stats.NodeInfo(nil), f.l.applied.pool.infos...)
	f.l.mu.Unlock()
	if len(infos) == 0 {
		t.Fatal("the fixture has no route nodes")
	}

	st := f.l.status("2026-10-22T09:00:00Z")
	if len(st.Activity) != 1 || st.Activity[0].Login != "alice" || st.Activity[0].Active || st.Activity[0].Connections != 0 {
		t.Fatalf("idle activity = %+v", st.Activity)
	}

	f.script.inst(0).activity = []core.AccountActivity{{
		Login: "alice", Connections: 2, Upload: 5, Download: 7,
		Nodes: []core.NodeActivity{{Label: infos[0].Label, Connections: 2, Upload: 5, Download: 7}},
	}}
	st = f.l.status("2026-10-22T09:00:00Z")
	if len(st.Activity) != 1 {
		t.Fatalf("activity = %+v", st.Activity)
	}
	a := st.Activity[0]
	if !a.Active || a.Connections != 2 || len(a.Nodes) != 1 || a.Nodes[0].Node != infos[0].Label ||
		a.Nodes[0].Provider != "p" || a.Nodes[0].Country != "DE" ||
		!reflect.DeepEqual(a.Providers, []string{"p"}) || !reflect.DeepEqual(a.Countries, []string{"DE"}) {
		t.Errorf("activity = %+v", a)
	}
}

func TestRunStatusPrintsActivity(t *testing.T) {
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	run := func(st angel.Status) map[string]any {
		angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
			return angel.State{}, st, nil
		}
		var out bytes.Buffer
		if err := runStatus([]string{"--home", t.TempDir()}, &out); err != nil {
			t.Fatal(err)
		}
		return decodeData(t, &out)
	}

	data := run(angel.Status{Activity: []angel.AccountActivity{
		{Login: "alice", Active: true, Connections: 2, Providers: []string{"main"}, Countries: []string{"DE"},
			Nodes: []angel.NodeUse{{Node: "main/a", Provider: "main", Country: "DE", Connections: 2}}},
		{Login: "bob", Providers: []string{}, Countries: []string{}, Nodes: []angel.NodeUse{}},
	}})
	if data["active_accounts"] != float64(1) {
		t.Errorf("active_accounts = %v, want 1", data["active_accounts"])
	}
	activity, _ := data["activity"].([]any)
	if len(activity) != 2 {
		t.Fatalf("activity = %v", data["activity"])
	}
	alice, _ := activity[0].(map[string]any)
	if alice["login"] != "alice" || alice["active"] != true || alice["connections"] != float64(2) {
		t.Errorf("alice = %v", alice)
	}
	nodes, _ := alice["nodes"].([]any)
	node, _ := nodes[0].(map[string]any)
	if node["node"] != "main/a" || node["provider"] != "main" || node["country"] != "DE" {
		t.Errorf("node = %v", node)
	}
	bob, _ := activity[1].(map[string]any)
	if bob["active"] != false || bob["nodes"] == nil {
		t.Errorf("bob = %v", bob)
	}

	data = run(angel.Status{})
	if _, has := data["activity"]; has {
		t.Error("activity is printed for an angel that did not report it")
	}
	if _, has := data["active_accounts"]; has {
		t.Error("active_accounts is printed for an angel that did not report activity")
	}
}

func TestNodeHealthLoggerWritesDownAndBack(t *testing.T) {
	var infoLines, warnLines []string
	info := func(format string, args ...any) { infoLines = append(infoLines, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { warnLines = append(warnLines, fmt.Sprintf(format, args...)) }
	hook := nodeHealthLogger([]stats.NodeInfo{
		{Label: "main/a", Provider: "main", Country: "US"},
		{Label: "main/b", Provider: "main"},
	}, info, warn)
	if hook == nil {
		t.Fatal("no hook for a route with nodes")
	}
	hook("main/a", true)
	hook("main/b", true)
	hook("main/a", false)
	wantWarn := []string{
		"node main/a (provider main, country US) is down",
		"node main/b (provider main, country unknown) is down",
	}
	if !reflect.DeepEqual(warnLines, wantWarn) {
		t.Errorf("warnings = %q, want %q", warnLines, wantWarn)
	}
	if !reflect.DeepEqual(infoLines, []string{"node main/a (provider main, country US) is back"}) {
		t.Errorf("info = %q", infoLines)
	}
}

func TestNodeHealthLoggerIncludesTheName(t *testing.T) {
	var infoLines, warnLines []string
	info := func(format string, args ...any) { infoLines = append(infoLines, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { warnLines = append(warnLines, fmt.Sprintf(format, args...)) }
	hook := nodeHealthLogger([]stats.NodeInfo{
		{Label: "own/a970e0d9fef1", Provider: "own", Country: "FI", Name: "Finland 1"},
	}, info, warn)
	if hook == nil {
		t.Fatal("no hook for a route with nodes")
	}
	hook("own/a970e0d9fef1", true)
	hook("own/a970e0d9fef1", false)
	if !reflect.DeepEqual(warnLines, []string{"node own/a970e0d9fef1 (Finland 1, provider own, country FI) is down"}) {
		t.Errorf("warnings = %q", warnLines)
	}
	if !reflect.DeepEqual(infoLines, []string{"node own/a970e0d9fef1 (Finland 1, provider own, country FI) is back"}) {
		t.Errorf("info = %q", infoLines)
	}
}

func TestNodeHealthLoggerWithoutNodes(t *testing.T) {
	if nodeHealthLogger(nil, nil, nil) != nil {
		t.Fatal("a hook for a route without nodes")
	}
}

func TestCoreOptionsCarryTheNodeHealthHook(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	if f.script.call(0).OnNodeHealth == nil {
		t.Fatal("the core was started without the node health hook")
	}
	f.l.mu.Lock()
	infos := append([]stats.NodeInfo(nil), f.l.applied.pool.infos...)
	f.l.mu.Unlock()
	f.script.call(0).OnNodeHealth(infos[0].Label, true)
	if !strings.Contains(f.logText(), "is down") || !strings.Contains(f.logText(), infos[0].Label) {
		t.Errorf("the hook wrote nothing useful: %q", f.logText())
	}
}
