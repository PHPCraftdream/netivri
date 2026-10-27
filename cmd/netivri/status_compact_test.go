package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	ktav "github.com/ktav-lang/golang"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// stubStatus installs an angelProbeLive stub that always answers with st.
func stubStatus(t *testing.T, st angel.Status) {
	t.Helper()
	old := angelProbeLive
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, st, nil
	}
	t.Cleanup(func() { angelProbeLive = old })
}

// runStatusData runs `status` with args and decodes the answer envelope.
func runStatusData(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := runStatus(append([]string{"--home", t.TempDir()}, args...), &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	return decodeData(t, &out)
}

func TestStatusChainComesFromRealProducerAndIsRedacted(t *testing.T) {
	status := statusAccountChain(
		"socks5://acct-user:acct-secret@account.example:1080",
		"",
		"socks5://unused-user:unused-secret@unused.example:1082",
		"socks5://global-user:global-secret@global.example:1081",
	)
	stubStatus(t, angel.Status{Accounts: []angel.AccountInfo{{Login: "alice", Chain: status}}})
	data := runStatusData(t)
	accounts := data["accounts"].([]any)
	account := accounts[0].(map[string]any)
	chain := account["chain"].(map[string]any)
	if chain["pre_socks5"] != "socks5://account.example:1080" || chain["pre_socks5_source"] != "account" || chain["pre_socks5_auth"] != true {
		t.Fatalf("account pre chain = %#v", chain)
	}
	if chain["post_socks5"] != "socks5://global.example:1081" || chain["post_socks5_source"] != "global" || chain["post_socks5_auth"] != true {
		t.Fatalf("global post chain = %#v", chain)
	}
	for _, secret := range []string{"acct-user", "acct-secret", "global-user", "global-secret", "unused-user", "unused-secret"} {
		if strings.Contains(mustJSON(t, data), secret) {
			t.Errorf("status leaked %q: %s", secret, mustJSON(t, data))
		}
	}
}

func TestStatusShowsAppliedChainWhileDiskHasPendingChain(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = append(cfg.Accounts, config.Account{Login: "alice", Password: config.InitPlaceholder,
		PreSOCKS5: "socks5://disk-user:disk-secret@disk.example:1080", PostSOCKS5: "none"})
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	diskBytes, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	live := &liveAngel{opts: upOptions{Home: home}, applied: desiredState{cfg: func() *config.Config {
		applied := config.Default()
		applied.Accounts = append(applied.Accounts, config.Account{Login: "alice", Password: config.InitPlaceholder,
			PreSOCKS5: "socks5://live-user:live-secret@live.example:1080", PostSOCKS5: ""})
		return applied
	}()}}
	status := live.status("")
	stubStatus(t, status)
	data := runStatusData(t)
	chain := data["accounts"].([]any)[0].(map[string]any)["chain"].(map[string]any)
	if chain["pre_socks5"] != "socks5://live.example:1080" || chain["post_socks5"] != "none" {
		t.Fatalf("status did not report applied chain: %#v", chain)
	}
	for _, secret := range []string{"disk-user", "disk-secret", "live-user", "live-secret"} {
		if strings.Contains(mustJSON(t, data), secret) {
			t.Errorf("status leaked %q: %s", secret, mustJSON(t, data))
		}
	}
	after, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diskBytes, after) {
		t.Fatal("status changed on-disk configuration")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatusShortOmitsFullWarmStatsAffinity(t *testing.T) {
	full := angel.Status{
		Warm: &angel.WarmInfo{
			Alive: 1, Total: 1, Probes: 3, ProbeFailures: 1,
			Nodes: []angel.NodeHealth{{Name: "netivri-DE-0", Alive: true, DelayMS: 7, Label: "main/de"}},
		},
		Stats:    &angel.StatsInfo{Path: "stats.db"},
		Affinity: &angel.AffinityInfo{Bindings: 2},
		Activity: []angel.AccountActivity{{Login: "alice", Active: true}},
		Accounts: []angel.AccountInfo{{Login: "alice"}},
	}
	stubStatus(t, full)
	data := runStatusData(t)
	if _, ok := data["stats"]; ok {
		t.Error("short status printed stats")
	}
	if _, ok := data["affinity"]; ok {
		t.Error("short status printed affinity")
	}
	rawWarm, ok := data["warm"].(map[string]any)
	if !ok {
		t.Fatalf("warm = %v", data["warm"])
	}
	if _, ok := rawWarm["nodes"]; ok {
		t.Error("short status printed warm.nodes")
	}
	for _, key := range []string{"alive", "total", "probes", "probe_failures"} {
		if _, ok := rawWarm[key]; !ok {
			t.Errorf("short warm misses %q: %v", key, rawWarm)
		}
	}
	if data["active_accounts"] != float64(1) {
		t.Errorf("active_accounts = %v", data["active_accounts"])
	}
	if _, ok := data["accounts"]; !ok {
		t.Error("short status omitted accounts")
	}

	fullData := runStatusData(t, "--full")
	for _, key := range []string{"stats", "affinity", "warm", "activity"} {
		if _, ok := fullData[key]; !ok {
			t.Errorf("full status misses %q", key)
		}
	}
	warm, ok := fullData["warm"].(map[string]any)
	if !ok {
		t.Fatalf("full warm = %v", fullData["warm"])
	}
	nodes, ok := warm["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("full warm.nodes = %v", warm["nodes"])
	}
	node, _ := nodes[0].(map[string]any)
	if node["delay_ms"] != float64(7) || node["label"] != "main/de" {
		t.Errorf("full warm node = %v", node)
	}
	if warm["alive"] != float64(1) || warm["total"] != float64(1) || warm["probes"] != float64(3) {
		t.Errorf("full warm counters = %v", warm)
	}
}

func TestWarmInfoOfCountsAliveAndTotal(t *testing.T) {
	info := warmInfoOf(core.WarmStats{
		Nodes: []core.NodeHealth{{Alive: true}, {Alive: true}, {}},
	}, map[string]string{}, nil)
	if info.Alive != 2 || info.Total != 3 {
		t.Errorf("alive/total = %d/%d, want 2/3", info.Alive, info.Total)
	}
}

func TestWarmInfoOfNodeDescriptors(t *testing.T) {
	info := warmInfoOf(core.WarmStats{
		Nodes: []core.NodeHealth{{Name: "netivri-r0-0", Alive: true}, {Name: "netivri-r1-0"}},
	}, map[string]string{"netivri-r0-0": "main/de", "netivri-r1-0": "main/nl"},
		[]stats.NodeInfo{{Label: "main/de", Provider: "main", Country: "DE", Name: "Germany 1"}})
	if info.Nodes[0].Label != "main/de" || info.Nodes[0].Provider != "main" ||
		info.Nodes[0].Country != "DE" || info.Nodes[0].NodeName != "Germany 1" {
		t.Errorf("node[0] = %+v", info.Nodes[0])
	}
	n := info.Nodes[1]
	if n.Provider != "" || n.Country != "" || n.NodeName != "" {
		t.Errorf("node without info = %+v, want empty descriptor", n)
	}
}

func TestStatusUnknownFlagIsUsage(t *testing.T) {
	stubStatus(t, angel.Status{})
	err := runStatus([]string{"--home", t.TempDir(), "--wat"}, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "USAGE" {
		t.Fatalf("err = %v (%T), want USAGE", err, err)
	}
}

func TestStatusShortKeepsEveryOtherTopLevelKey(t *testing.T) {
	st := angel.Status{
		Warm:     &angel.WarmInfo{Probes: 1},
		Stats:    &angel.StatsInfo{Path: "stats.db"},
		Affinity: &angel.AffinityInfo{Bindings: 1},
		Refresh:  &angel.RefreshInfo{IntervalSeconds: 3600},
		Reload:   &angel.ReloadInfo{Count: 1},
	}
	stubStatus(t, st)
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatal(err)
	}
	data := runStatusData(t)
	for key := range full {
		switch key {
		case "warm", "stats", "affinity":
			continue
		}
		if _, ok := data[key]; !ok {
			t.Errorf("short status lost top-level key %q", key)
		}
	}
}

func TestStatusFullMatchesShortKeySetPlusFullKeys(t *testing.T) {
	st := angel.Status{
		Listen:   "127.0.0.1:7890",
		Route:    "direct",
		Warm:     &angel.WarmInfo{Probes: 1},
		Stats:    &angel.StatsInfo{Path: "stats.db"},
		Affinity: &angel.AffinityInfo{Bindings: 1},
	}
	stubStatus(t, st)
	short := runStatusData(t)
	full := runStatusData(t, "--full")
	for _, key := range []string{"version", "listen", "route"} {
		if !reflect.DeepEqual(short[key], full[key]) {
			t.Errorf("%s: short %v, full %v", key, short[key], full[key])
		}
	}
}

// runStatusKtavDoc runs `status` with args under the Ktav renderer and
// returns the parsed document.
func runStatusKtavDoc(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := runStatus(append([]string{"--home", t.TempDir()}, args...), &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	got, err := ktav.Loads(out.String())
	if err != nil {
		t.Fatalf("output is not Ktav: %v\n%s", err, out.String())
	}
	return got.(map[string]any)
}

// ktavStatus is the stubbed angel status shared by the Ktav status tests.
func ktavStatus() angel.Status {
	return angel.Status{
		Warm: &angel.WarmInfo{
			Alive: 2, Total: 3, Probes: 9, ProbeFailures: 1,
			Nodes: []angel.NodeHealth{{Name: "netivri-DE-0", Alive: true, DelayMS: 7, Label: "main/de"}},
		},
		Stats:    &angel.StatsInfo{Path: "stats.db"},
		Affinity: &angel.AffinityInfo{Bindings: 2},
	}
}

func TestStatusKtavShortOmitsWarmNodesStatsAffinity(t *testing.T) {
	useKtavOutput(t)
	stubStatus(t, ktavStatus())
	doc := runStatusKtavDoc(t)
	if doc["ok"] != true || doc["schema_version"] != int64(cli.SchemaVersion) {
		t.Fatalf("envelope header = %v / %v", doc["ok"], doc["schema_version"])
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %v", doc["data"])
	}
	warm, ok := data["warm"].(map[string]any)
	if !ok {
		t.Fatalf("warm = %v", data["warm"])
	}
	if warm["alive"] != int64(2) || warm["total"] != int64(3) || warm["probes"] != int64(9) {
		t.Errorf("warm = %v", warm)
	}
	for _, key := range []string{"nodes", "stats", "affinity"} {
		if _, has := data[key]; has {
			t.Errorf("short Ktav status printed %q", key)
		}
	}
	if _, has := warm["nodes"]; has {
		t.Error("short Ktav status printed warm.nodes")
	}
}

func TestStatusKtavFullKeepsStatsAffinityWarmNodes(t *testing.T) {
	useKtavOutput(t)
	stubStatus(t, ktavStatus())
	doc := runStatusKtavDoc(t, "--full")
	if doc["ok"] != true || doc["schema_version"] != int64(cli.SchemaVersion) {
		t.Fatalf("envelope header = %v / %v", doc["ok"], doc["schema_version"])
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %v", doc["data"])
	}
	stats, ok := data["stats"].(map[string]any)
	if !ok || stats["path"] != "stats.db" {
		t.Errorf("stats = %v", data["stats"])
	}
	affinity, ok := data["affinity"].(map[string]any)
	if !ok || affinity["bindings"] != int64(2) {
		t.Errorf("affinity = %v", data["affinity"])
	}
	warm, ok := data["warm"].(map[string]any)
	if !ok {
		t.Fatalf("warm = %v", data["warm"])
	}
	nodes, ok := warm["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("warm.nodes = %v", warm["nodes"])
	}
	node := nodes[0].(map[string]any)
	if node["name"] != "netivri-DE-0" || node["delay_ms"] != int64(7) {
		t.Errorf("node = %v", node)
	}
}

func TestStatusKeepsCountersAbove53Bits(t *testing.T) {
	const probes = uint64(9007199254740993) // 2^53 + 1
	stubStatus(t, angel.Status{Warm: &angel.WarmInfo{Probes: probes}})

	var out bytes.Buffer
	if err := runStatus([]string{"--home", t.TempDir()}, &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(out.String(), `"probes":9007199254740993`) {
		t.Errorf("JSON output lost precision: %s", out.String())
	}

	out.Reset()
	if err := runStatus([]string{"--home", t.TempDir(), "--full"}, &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(out.String(), `"probes":9007199254740993`) {
		t.Errorf("full JSON output lost precision: %s", out.String())
	}

	useKtavOutput(t)
	data := runStatusKtavDoc(t)["data"].(map[string]any)
	warm := data["warm"].(map[string]any)
	if warm["probes"] != int64(9007199254740993) {
		t.Errorf("Ktav warm.probes = %v (%T)", warm["probes"], warm["probes"])
	}
}
