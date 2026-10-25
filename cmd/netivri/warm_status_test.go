package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
)

func TestWarmOptionsOnlyWithCountryNodes(t *testing.T) {
	if got := warmOptions(nil); got != nil {
		t.Errorf("warmOptions(nil) = %+v, want nil", got)
	}
	if got := warmOptions(map[string][]map[string]any{}); got != nil {
		t.Errorf("warmOptions(empty) = %+v, want nil", got)
	}
	got := warmOptions(map[string][]map[string]any{"DE": {{"type": "socks5"}}})
	if got == nil || got.PoolSize != 0 {
		t.Errorf("warmOptions(DE) = %+v, want probes without a pool", got)
	}
}

func TestWarmInfoOf(t *testing.T) {
	info := warmInfoOf(core.WarmStats{
		Probes: 5, ProbeFailures: 2, PoolHits: 3, PoolMisses: 1, PoolDiscards: 4,
		Nodes: []core.NodeHealth{
			{Name: "netivri-r0-0", Alive: true, Delay: 48 * time.Millisecond},
			{Name: "netivri-r1-0"},
		},
	}, map[string]string{"netivri-r0-0": "main/de", "netivri-r1-0": "main/nl"})
	if info.Probes != 5 || info.ProbeFailures != 2 || info.PoolHits != 3 || info.PoolMisses != 1 || info.PoolDiscards != 4 {
		t.Errorf("counters = %+v", info)
	}
	want := []daemon.NodeHealth{
		{Name: "netivri-r0-0", Alive: true, DelayMS: 48, Label: "main/de"},
		{Name: "netivri-r1-0", Label: "main/nl"},
	}
	if len(info.Nodes) != len(want) || info.Nodes[0] != want[0] || info.Nodes[1] != want[1] {
		t.Errorf("nodes = %+v, want %+v", info.Nodes, want)
	}
}

func TestStatusPrintsWarmOnlyWhenPresent(t *testing.T) {
	old := daemonProbeLive
	t.Cleanup(func() { daemonProbeLive = old })

	run := func(st daemon.Status) map[string]json.RawMessage {
		daemonProbeLive = func(context.Context, string) (daemon.State, daemon.Status, error) {
			return daemon.State{}, st, nil
		}
		var out bytes.Buffer
		if err := runStatus([]string{"--home", t.TempDir()}, &out); err != nil {
			t.Fatalf("runStatus: %v", err)
		}
		var env struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("decode %s: %v", out.String(), err)
		}
		return env.Data
	}

	if _, ok := run(daemon.Status{})["warm"]; ok {
		t.Error("status printed warm for an instance without warm-up")
	}
	data := run(daemon.Status{Warm: &daemon.WarmInfo{
		Probes: 1,
		Nodes:  []daemon.NodeHealth{{Name: "netivri-DE-0", Alive: true, DelayMS: 7}},
	}})
	raw, ok := data["warm"]
	if !ok {
		t.Fatal("status omitted warm")
	}
	var warm daemon.WarmInfo
	if err := json.Unmarshal(raw, &warm); err != nil {
		t.Fatalf("decode warm: %v", err)
	}
	if warm.Probes != 1 || len(warm.Nodes) != 1 || warm.Nodes[0].DelayMS != 7 {
		t.Errorf("warm = %+v", warm)
	}
}
