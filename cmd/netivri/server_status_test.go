package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

func TestStatusWatchServersTextAndJSON(t *testing.T) {
	st := angel.Status{Listen: "127.0.0.1:7890", StartedAt: "2026-01-02T03:04:05Z", Servers: []angel.ServerInfo{{Name: "edge", Protocol: "ss", Listen: "127.0.0.1:9", Accounts: []string{"alice"}, Connections: 2, DroppedClients: 3}}}
	var text bytes.Buffer
	(&watchPrinter{out: &text}).frameText(st, nil, time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC))
	wantText := "netivri status --watch  listen=127.0.0.1:7890  uptime=2s  active_accounts=0\nLOGIN  STATE  CONN  UP/s  DOWN/s  NODES  COUNTRIES\nServers\nNAME  PROTOCOL  LISTEN       ACCOUNTS  CONN  DROPPED\nedge  ss        127.0.0.1:9  alice     2     3\n"
	if text.String() != wantText {
		t.Fatalf("watch text mismatch\n got: %q\nwant: %q", text.String(), wantText)
	}

	var out bytes.Buffer
	(&watchPrinter{out: &out, jsonOut: true}).frameJSON(st, nil, time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC))
	wantJSON := `{"time":"2026-01-02T03:04:07Z","active_accounts":0,"accounts":[],"servers":[{"name":"edge","protocol":"ss","listen":"127.0.0.1:9","accounts":["alice"],"connections":2,"dropped_clients":3}]}` + "\n"
	if out.String() != wantJSON {
		t.Fatalf("watch JSON mismatch\n got: %s\nwant: %s", out.String(), wantJSON)
	}
	out.Reset()
	(&watchPrinter{out: &out, jsonOut: true}).frameJSON(angel.Status{}, nil, time.Time{})
	if strings.Contains(out.String(), `"servers"`) {
		t.Fatalf("empty servers included: %s", out.String())
	}
}

func TestLiveStatusServerInfoMapping(t *testing.T) {
	f := newReloadFixture(t, upOptions{})
	fake := &statusServerCore{coreInstance: f.script.inst(0), infos: []servers.Info{
		{Name: "zeta", Protocol: "vmess", Listen: "127.0.0.1:2", Accounts: []string{"bob"}, Connections: 4, DroppedClients: 8},
		{Name: "alpha", Protocol: "ss", Listen: "127.0.0.1:1", Accounts: []string{"alice"}, Connections: 1, DroppedClients: 2},
	}}
	f.l.inst = fake
	got := f.l.status("2026-01-02T03:04:05Z").Servers
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("server ordering: %#v", got)
	}
	if got[0].Connections != 1 || got[0].DroppedClients != 2 || got[0].Accounts[0] != "alice" {
		t.Fatalf("server mapping: %#v", got[0])
	}
	got[0].Accounts[0] = "changed"
	if fake.infos[1].Accounts[0] != "alice" {
		t.Fatal("status accounts alias source slice")
	}
}

type statusServerCore struct {
	coreInstance
	infos []servers.Info
}

func (f *statusServerCore) Servers() []servers.Info { return f.infos }

func TestLiveStatusOmitsServersWithoutOptionalSource(t *testing.T) {
	f := newReloadFixture(t, upOptions{})
	f.l.inst = f.script.inst(0)
	st := f.l.status("2026-01-02T03:04:05Z")
	if len(st.Servers) != 0 {
		t.Fatalf("unexpected servers: %#v", st.Servers)
	}
}

func TestRunStatusPreservesServerJSON(t *testing.T) {
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	st := angel.Status{Servers: []angel.ServerInfo{{Name: "edge", Protocol: "ss", Listen: "127.0.0.1:9", Accounts: []string{"alice"}, Connections: 2, DroppedClients: 3}}}
	angelProbeLive = func(_ context.Context, _ string) (angel.State, angel.Status, error) { return angel.State{}, st, nil }
	const want = `[{"name":"edge","protocol":"ss","listen":"127.0.0.1:9","accounts":["alice"],"connections":2,"dropped_clients":3}]`
	home := t.TempDir()
	for _, args := range [][]string{{"--home", home, "--json"}, {"--home", home, "--json", "--full"}} {
		var out bytes.Buffer
		if err := runStatus(args, &out); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var actualValue, wantValue any
		if err := json.Unmarshal(got.Data["servers"], &actualValue); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
			t.Fatal(err)
		}
		actual, err := json.Marshal(actualValue)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := json.Marshal(wantValue)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("args %v servers JSON = %s, want %s", args, actual, expected)
		}
	}
	angelProbeLive = func(_ context.Context, _ string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, nil
	}
	var out bytes.Buffer
	if err := runStatus([]string{"--home", home, "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Data["servers"]; ok {
		t.Fatalf("servers should be omitted: %s", out.String())
	}
}
