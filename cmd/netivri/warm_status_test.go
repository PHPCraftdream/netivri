package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
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
			{Name: "netivri-r1-0", Down: true},
		},
	}, map[string]string{"netivri-r0-0": "main/de", "netivri-r1-0": "main/nl"}, []stats.NodeInfo{
		{Label: "main/de", Provider: "main", Country: "DE", Name: "Germany 1"},
		{Label: "main/nl", Provider: "main", Country: "NL"},
	})
	if info.Probes != 5 || info.ProbeFailures != 2 || info.PoolHits != 3 || info.PoolMisses != 1 || info.PoolDiscards != 4 {
		t.Errorf("counters = %+v", info)
	}
	if info.Alive != 1 || info.Total != 2 {
		t.Errorf("alive/total = %d/%d, want 1/2", info.Alive, info.Total)
	}
	want := []angel.NodeHealth{
		{Name: "netivri-r0-0", Alive: true, DelayMS: 48, Label: "main/de", Provider: "main", Country: "DE", NodeName: "Germany 1"},
		{Name: "netivri-r1-0", Label: "main/nl", Provider: "main", Country: "NL"},
	}
	// The routing state is carried as a pointer: check it, then compare the rest.
	if len(info.Nodes) != len(want) {
		t.Fatalf("nodes = %+v, want %d", info.Nodes, len(want))
	}
	if d := info.Nodes[0].Down; d == nil || *d {
		t.Errorf("node 0 Down = %v, want false", d)
	}
	if d := info.Nodes[1].Down; d == nil || !*d {
		t.Errorf("node 1 Down = %v, want true", d)
	}
	info.Nodes[0].Down, info.Nodes[1].Down = nil, nil
	if info.Nodes[0] != want[0] || info.Nodes[1] != want[1] {
		t.Errorf("nodes = %+v, want %+v", info.Nodes, want)
	}
}

func TestStatusPrintsWarmOnlyWhenPresent(t *testing.T) {
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })

	run := func(st angel.Status, extra ...string) map[string]json.RawMessage {
		angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
			return angel.State{}, st, nil
		}
		var out bytes.Buffer
		if err := runStatus(append([]string{"--home", t.TempDir()}, extra...), &out); err != nil {
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

	if _, ok := run(angel.Status{})["warm"]; ok {
		t.Error("status printed warm for an instance without warm-up")
	}
	data := run(angel.Status{Warm: &angel.WarmInfo{
		Alive: 1, Total: 1, Probes: 1,
		Nodes: []angel.NodeHealth{{Name: "netivri-DE-0", Alive: true, DelayMS: 7}},
	}})
	raw, ok := data["warm"]
	if !ok {
		t.Fatal("status omitted warm")
	}
	var warm struct {
		Alive         int    `json:"alive"`
		Total         int    `json:"total"`
		Probes        uint64 `json:"probes"`
		ProbeFailures uint64 `json:"probe_failures"`
	}
	if err := json.Unmarshal(raw, &warm); err != nil {
		t.Fatalf("decode warm: %v", err)
	}
	if warm.Probes != 1 || warm.Alive != 1 || warm.Total != 1 {
		t.Errorf("warm = %+v", warm)
	}
	if _, ok := rawMap(data, "warm")["nodes"]; ok {
		t.Error("short status printed warm.nodes")
	}
	data = run(angel.Status{Warm: &angel.WarmInfo{
		Alive: 1, Total: 1, Probes: 1,
		Nodes: []angel.NodeHealth{{Name: "netivri-DE-0", Alive: true, DelayMS: 7}},
	}}, "--full")
	raw, ok = data["warm"]
	if !ok {
		t.Fatal("status omitted warm")
	}
	var fullWarm angel.WarmInfo
	if err := json.Unmarshal(raw, &fullWarm); err != nil {
		t.Fatalf("decode warm: %v", err)
	}
	if fullWarm.Probes != 1 || len(fullWarm.Nodes) != 1 || fullWarm.Nodes[0].DelayMS != 7 ||
		fullWarm.Alive != 1 || fullWarm.Total != 1 {
		t.Errorf("warm = %+v", fullWarm)
	}
}

// rawMap decodes one raw JSON object of data into a map.
func rawMap(data map[string]json.RawMessage, key string) map[string]json.RawMessage {
	raw, ok := data[key]
	if !ok {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
