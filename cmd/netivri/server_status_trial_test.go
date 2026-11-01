package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

func withServerTrialStatus(t *testing.T, status angel.Status, err error) {
	t.Helper()
	old := angelProbeLive
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, status, err
	}
	t.Cleanup(func() { angelProbeLive = old })
}

func TestServerListAndShowOverlayRuntimeCounters(t *testing.T) {
	status := angel.Status{Servers: []angel.ServerInfo{{Name: "edge", Unclassified: 1, SlowFirstByte: 2, Replayed: 3, Ambiguous: 4}, {Name: "removed", Replayed: 99}}}
	withServerTrialStatus(t, status, nil)
	cli.UseJSON()
	t.Cleanup(func() { cli.UseJSON() })
	for _, command := range []string{"list", "show"} {
		home := t.TempDir()
		cfg := config.Default()
		cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
		cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 12345, Accounts: "alice"}}
		if err := config.Save(config.ConfigPath(home), cfg); err != nil {
			t.Fatal(err)
		}
		args := []string{command, "--home", home}
		if command == "show" {
			args = append(args, "edge")
		}
		var out strings.Builder
		if err := runServer(args, &out); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
			t.Fatal(err)
		}
		var view serverView
		if command == "list" {
			var list []serverView
			if err := json.Unmarshal(doc.Data["servers"], &list); err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 {
				t.Fatalf("list includes stale runtime servers: %#v", list)
			}
			view = list[0]
		} else if err := json.Unmarshal(doc.Data["server"], &view); err != nil {
			t.Fatal(err)
		}
		if view.Unclassified != 1 || view.SlowFirstByte != 2 || view.Replayed != 3 || view.Ambiguous != 4 {
			t.Fatalf("%s counters = %#v", command, view)
		}
	}
}

func TestServerListFallsBackToConfigAndOmitsZeroCounters(t *testing.T) {
	cli.UseJSON()
	t.Cleanup(func() { cli.UseJSON() })
	withServerTrialStatus(t, angel.Status{}, angel.ErrNotRunning)
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 12345, Accounts: "alice"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runServer([]string{"list", "--home", home}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"unclassified"`) || strings.Contains(out.String(), `"slow_first_byte"`) || strings.Contains(out.String(), `"replayed"`) || strings.Contains(out.String(), `"ambiguous"`) {
		t.Fatalf("zero counters not omitted: %s", out.String())
	}
}

func TestDoctorServerCounterSummary(t *testing.T) {
	home := serverTestHome(t)
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 12345, Accounts: "alice"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	specs, err := cfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := servers.NewStore(home).EnsureSecrets(specs[0]); err != nil {
		t.Fatal(err)
	}
	d := &doctorContext{home: home, cfg: cfg, angelUp: true, status: angel.Status{Servers: []angel.ServerInfo{{Name: "edge", Unclassified: 8, SlowFirstByte: 9, Replayed: 10, Ambiguous: 11}, {Name: "stale", Replayed: 99}}}}
	oldListen := doctorListen
	doctorListen = func(string) error { return nil }
	t.Cleanup(func() { doctorListen = oldListen })
	checks := d.checkServers()
	found := false
	for _, check := range checks {
		if check.Name == "server-counters-stale" {
			t.Fatal("doctor summarized stale removed server")
		}
		if check.Name == "server-counters-edge" {
			found = true
			for _, expected := range []string{"unclassified=8", "slow_first_byte=9", "replayed=10", "ambiguous=11"} {
				if !strings.Contains(check.Detail, expected) {
					t.Fatalf("counter detail %q missing %q", check.Detail, expected)
				}
			}
		}
	}
	if !found {
		t.Fatalf("doctor checks lack runtime counter summary: %+v", checks)
	}
}
