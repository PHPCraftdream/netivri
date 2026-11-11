package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/stats"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestLiveNodeLoggerBuilderDoesNotReplaceActiveLogger(t *testing.T) {
	l := newLiveAngel(upOptions{}, func(string, ...any) {}, func() {})
	old := makeNodeHealthLogger([]stats.NodeInfo{{Label: "p/old", Country: "FI"}}, nil, nil, nil, nil, nil, time.Now)
	l.installNodeLogger(old)
	d := testDesired()
	d.pool.infos = []stats.NodeInfo{{Label: "p/new", Country: "US"}}
	o, candidate, err := l.routeOptionsWithLogger(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil || o.OnNodeHealth == nil {
		t.Fatal("builder did not create candidate logger and hook")
	}
	if l.activeNodeLogger() != old {
		t.Fatal("building candidate replaced active logger")
	}
	o.OnNodeHealth(core.NodeEvent{Label: "p/new", Down: true, NeverOK: true, Reason: "timeout"})
	if len(candidate.neverLabels) != 1 || len(old.neverLabels) != 0 || l.activeNodeLogger() != old {
		t.Fatal("candidate hook did not stay isolated")
	}
	d.pool.infos = nil
	o, candidate, err = l.routeOptionsWithLogger(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate != nil || o.OnNodeHealth != nil || l.activeNodeLogger() != old {
		t.Fatal("empty candidate changed active logger or installed a hook")
	}
}

func TestLiveNodeLoggerReconfigureKeepsOutageState(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	var nodes []subscription.Node
	for i := 0; i < 7; i++ {
		name, server := fmt.Sprintf("node-%d", i), fmt.Sprintf("192.0.2.%d", i+1)
		nodes = append(nodes, subscription.Node{ID: fmt.Sprintf("n%d", i), Name: name, Type: "socks5", Server: server, Port: 1080, Country: "DE",
			Raw: map[string]any{"name": name, "type": "socks5", "server": server, "port": 1080}})
	}
	store := subscription.NewStore(filepath.Join(f.home, "subscriptions"))
	if err := store.Save("p", subscription.Snapshot{FetchedAt: time.Now().UTC(), Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	d, err := f.l.load()
	if err != nil {
		t.Fatal(err)
	}
	logLine := func(level, format string, args ...any) {
		f.mu.Lock()
		f.logs = append(f.logs, level+fmt.Sprintf(format, args...))
		f.mu.Unlock()
	}
	f.l.logf = func(format string, args ...any) { logLine("INFO ", format, args...) }
	f.l.warnf = func(format string, args ...any) { logLine("WARN ", format, args...) }
	f.l.debugf = func(format string, args ...any) { logLine("DEBUG ", format, args...) }
	f.script.inst(0).mu.Lock()
	f.script.inst(0).inPlace = true
	f.script.inst(0).mu.Unlock()
	initialLogger := f.l.activeNodeLogger()
	if !f.l.reconfigure(d) {
		t.Fatal("initial node-table update was not accepted in place")
	}
	active := f.l.activeNodeLogger()
	if active == nil || active != initialLogger || len(active.byLabel) != 7 {
		t.Fatalf("active node logger = %v, want retained seven-node logger", active)
	}
	active.info = func(format string, args ...any) { logLine("INFO ", format, args...) }
	active.warn = func(format string, args ...any) { logLine("WARN ", format, args...) }
	active.debug = func(format string, args ...any) { logLine("DEBUG ", format, args...) }
	clock := time.Now().Add(10 * time.Minute)
	active.now = func() time.Time { return clock }
	for i := 0; i < 6; i++ {
		active.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, Reason: "timeout"})
		clock = clock.Add(time.Minute)
	}
	if !active.outage || len(active.downNow) != 6 {
		t.Fatalf("before reconfigure: outage=%v down=%d", active.outage, len(active.downNow))
	}

	// A changed node address and name are applied in place without replacing
	// the active health logger, and its metadata is refreshed on success.
	nodes[6].Server, nodes[6].Name = "198.51.100.7", "renamed-node"
	nodes[6].Raw["server"], nodes[6].Raw["name"] = nodes[6].Server, nodes[6].Name
	if err := store.Save("p", subscription.Snapshot{FetchedAt: time.Now().UTC(), Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	f.l.reload(t.Context())
	if f.script.calls() != 1 || f.reloads() != 2 || f.l.activeNodeLogger() != active {
		t.Fatalf("successful in-place reload replaced the core/logger: starts=%d reloads=%d sameLogger=%v", f.script.calls(), f.reloads(), f.l.activeNodeLogger() == active)
	}
	if active.byLabel["p/n6"].Name != "renamed-node" || !active.outage || len(active.downNow) != 6 {
		t.Fatalf("refresh lost outage or metadata: outage=%v down=%d node=%+v", active.outage, len(active.downNow), active.byLabel["p/n6"])
	}
	reconfigs := f.script.inst(0).reconfigured()
	if len(reconfigs) != 2 || reconfigs[1].OnNodeHealth == nil {
		t.Fatalf("successful reconfigure hook missing: count=%d", len(reconfigs))
	}
	hook := reconfigs[1].OnNodeHealth

	// A rejected server-only reconfiguration must not apply candidate metadata
	// or discard any active outage state.
	beforeDown := len(active.downNow)
	f.script.inst(0).mu.Lock()
	f.script.inst(0).reconfErr = fmt.Errorf("reconfigure rejected")
	f.script.inst(0).mu.Unlock()
	f.l.mu.Lock()
	rejected := f.l.applied
	f.l.mu.Unlock()
	rejected.pool.infos = append([]stats.NodeInfo(nil), rejected.pool.infos...)
	rejected.pool.infos[6].Name = "rejected-metadata"
	rejected.servers = append(rejected.servers, servers.Spec{Name: "test-server", Protocol: "vmess", Bind: "127.0.0.1", Port: 0})
	if !f.l.reconfigure(rejected) {
		t.Fatal("server-only rejection should be handled without a restart")
	}
	if f.l.activeNodeLogger() != active || active.byLabel["p/n6"].Name != "renamed-node" || !active.outage || len(active.downNow) != beforeDown {
		t.Fatalf("failed reconfigure changed active logger state: outage=%v down=%d node=%+v", active.outage, len(active.downNow), active.byLabel["p/n6"])
	}

	priorWarns := strings.Count(f.logText(), "WARN ")
	hook(core.NodeEvent{Label: "p/n6", Down: true, Reason: "timeout"})
	if got := strings.Count(f.logText(), "WARN "); got != priorWarns {
		t.Fatalf("down during retained outage added a warning: before=%d after=%d logs=%q", priorWarns, got, f.logText())
	}
	for i := 0; i < 6; i++ {
		hook(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Downtime: time.Minute})
	}
	lines := strings.Split(f.logText(), "\n")
	if got := lines[len(lines)-1]; !strings.HasPrefix(got, "INFO node outage over: up to 7 of 7 servers were down, 1 still down, after ") {
		t.Fatalf("retained outage recovery summary missing: %q", f.logText())
	}
}

type reconfigureEventCore struct {
	*fakeCore
	event core.NodeEvent
}

func (f *reconfigureEventCore) configure(event core.NodeEvent, err error) {
	f.mu.Lock()
	f.event, f.reconfErr = event, err
	f.mu.Unlock()
}

func (f *reconfigureEventCore) Reconfigure(o core.Options) error {
	f.mu.Lock()
	f.reconfs = append(f.reconfs, o)
	err, inPlace, event := f.reconfErr, f.inPlace, f.event
	f.mu.Unlock()
	if event.Label != "" && o.OnNodeHealth != nil {
		o.OnNodeHealth(event)
	}
	if err != nil {
		return err
	}
	if !inPlace {
		return core.ErrReconfigureUnsupported
	}
	return nil
}

func TestLiveReloadQueuesNewNodeEventsUntilMetadataRefresh(t *testing.T) {
	f := newReloadFixture(t, upOptions{}, config.Account{Login: "alice", Provider: "p", ExitCountry: "DE"})
	nodes := make([]subscription.Node, 0, 9)
	for i := 0; i < 7; i++ {
		name, server := fmt.Sprintf("node-%d", i), fmt.Sprintf("192.0.2.%d", i+1)
		nodes = append(nodes, subscription.Node{ID: fmt.Sprintf("n%d", i), Name: name, Type: "socks5", Server: server, Port: 1080, Country: "DE",
			Raw: map[string]any{"name": name, "type": "socks5", "server": server, "port": 1080}})
	}
	store := subscription.NewStore(filepath.Join(f.home, "subscriptions"))
	saveNodes := func() {
		t.Helper()
		if err := store.Save("p", subscription.Snapshot{FetchedAt: time.Unix(50_000, 0).UTC(), Nodes: nodes}); err != nil {
			t.Fatal(err)
		}
	}
	saveNodes()
	d, err := f.l.load()
	if err != nil {
		t.Fatal(err)
	}
	base := f.script.inst(0)
	base.mu.Lock()
	base.inPlace = true
	base.mu.Unlock()
	inst := &reconfigureEventCore{fakeCore: base}
	f.l.mu.Lock()
	f.l.inst = inst
	f.l.mu.Unlock()
	if !f.l.reconfigure(d) {
		t.Fatal("initial node-table update was not accepted in place")
	}
	active := f.l.activeNodeLogger()
	if active == nil || len(active.byLabel) != 7 {
		t.Fatalf("active logger labels = %v, want seven", active)
	}
	var nodeLines []string
	clock := time.Unix(60_000, 0)
	active.info = func(format string, args ...any) { nodeLines = append(nodeLines, "INFO "+fmt.Sprintf(format, args...)) }
	active.warn = func(format string, args ...any) { nodeLines = append(nodeLines, "WARN "+fmt.Sprintf(format, args...)) }
	active.debug = func(format string, args ...any) { nodeLines = append(nodeLines, "DEBUG "+fmt.Sprintf(format, args...)) }
	active.now = func() time.Time { return clock }
	active.event(core.NodeEvent{Label: "p/n0", Down: true, Reason: "timeout"})
	active.event(core.NodeEvent{Label: "p/n1", Down: true, NeverOK: true, Reason: "timeout"})

	nodes = append(nodes, subscription.Node{ID: "n7", Name: "new-label", Type: "socks5", Server: "192.0.2.8", Port: 1080, Country: "DE",
		Raw: map[string]any{"name": "new-label", "type": "socks5", "server": "192.0.2.8", "port": 1080}})
	saveNodes()
	inst.configure(core.NodeEvent{Label: "p/n7", Down: true, NeverOK: true, Reason: "timeout"}, nil)
	f.l.reload(t.Context())
	if f.l.activeNodeLogger() != active {
		t.Fatal("successful reload replaced the active health logger")
	}
	if got := active.byLabel["p/n7"]; got.Name != "new-label" || got.Country != "DE" {
		t.Errorf("new-label metadata = %+v, want name and country from refreshed routes", got)
	}
	if _, ok := active.downNow["n0"]; !ok {
		t.Error("pre-reload down state was lost")
	}
	if _, ok := active.never["n1"]; !ok {
		t.Error("pre-reload never-answered state was lost")
	}
	if _, ok := active.never["n7"]; !ok || active.neverMeta["n7"].label != "p/n7" {
		t.Errorf("queued new-label event was not applied after refresh: never=%v meta=%v", active.never, active.neverMeta)
	}
	wantNever := "INFO node p/n7 (new-label, provider p, country DE) has never answered: timeout"
	if got := nodeLines[len(nodeLines)-1]; got != wantNever {
		t.Errorf("queued event line = %q, want %q", got, wantNever)
	}

	hook := inst.reconfigured()[len(inst.reconfigured())-1].OnNodeHealth
	clock = clock.Add(time.Minute)
	hook(core.NodeEvent{Label: "p/n7", Downtime: time.Minute})
	if got := nodeLines[len(nodeLines)-1]; got != "INFO node p/n7 (new-label, provider p, country DE) is back after 1m0s down" {
		t.Errorf("installed callback after reload = %q", got)
	}

	nodes = append(nodes, subscription.Node{ID: "n8", Name: "rejected-label", Type: "socks5", Server: "192.0.2.9", Port: 1080, Country: "DE",
		Raw: map[string]any{"name": "rejected-label", "type": "socks5", "server": "192.0.2.9", "port": 1080}})
	saveNodes()
	rejected, err := f.l.load()
	if err != nil {
		t.Fatal(err)
	}
	inst.configure(core.NodeEvent{Label: "p/n8", Down: true, NeverOK: true, Reason: "timeout"}, fmt.Errorf("reconfigure rejected"))
	beforeLines := len(nodeLines)
	if f.l.reconfigure(rejected) {
		t.Fatal("rejected route candidate was reported as applied")
	}
	if _, ok := active.byLabel["p/n8"]; ok || active.byLabel["p/n7"].Name != "new-label" {
		t.Errorf("rejected candidate metadata changed active logger: n7=%+v n8=%+v", active.byLabel["p/n7"], active.byLabel["p/n8"])
	}
	if _, ok := active.never["n8"]; ok || len(nodeLines) != beforeLines {
		t.Errorf("rejected candidate event changed active logger state: never=%v lines=%q", active.never, nodeLines[beforeLines:])
	}
	if _, ok := active.downNow["n0"]; !ok {
		t.Error("rejected candidate discarded retained down state")
	}
	if _, ok := active.never["n1"]; !ok {
		t.Error("rejected candidate discarded retained never-answered state")
	}
}

func TestLiveNodeLoggerInstallAndFlushUseActiveLogger(t *testing.T) {
	now := time.Unix(10000, 0)
	var lines []string
	mk := func() *nodeLog {
		return makeNodeHealthLogger([]stats.NodeInfo{{Label: "p/a", Country: "FI"}, {Label: "p/b", Country: "US"}, {Label: "p/c", Country: "DE"}, {Label: "p/d", Country: "NL"}, {Label: "p/e", Country: "SE"}}, nil, nil,
			func(f string, a ...any) { lines = append(lines, "DEBUG "+fmt.Sprintf(f, a...)) },
			func(f string, a ...any) { lines = append(lines, "INFO "+fmt.Sprintf(f, a...)) }, nil, func() time.Time { return now })
	}
	old := mk()
	l := &liveAngel{}
	l.installNodeLogger(old)
	for i := 0; i < 5; i++ {
		old.event(core.NodeEvent{Label: fmt.Sprintf("p/%c", 'a'+i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	if old.neverSummary != 3 {
		t.Fatalf("summary baseline = %d, want 3", old.neverSummary)
	}
	l.flushNodeLogger()
	if len(lines) != 6 {
		t.Fatalf("active tick lines = %d, want 6", len(lines))
	}
	candidate := mk() // constructing a rejected candidate must not affect the active logger
	if l.activeNodeLogger() != old {
		t.Fatal("candidate construction replaced active logger")
	}
	l.installNodeLogger(old) // same pointer must not flush/reset
	if l.activeNodeLogger() != old || old.neverLabels == nil {
		t.Fatal("same logger installation reset active window")
	}
	// Replacement force-flushes the pending five-server summary.
	l.installNodeLogger(candidate)
	if l.activeNodeLogger() != candidate {
		t.Fatal("successful logger replacement was not installed")
	}
	count := 0
	for _, line := range lines {
		if line == "INFO 5 servers have never answered since start (timeout x5; DE x1, FI x1, NL x1, SE x1, US x1); probed rarely, one DEBUG line each" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("forced summary count = %d, lines=%q", count, lines)
	}
	before := len(lines)
	l.flushNodeLogger()
	if len(lines) != before {
		t.Fatalf("tick after replacement changed lines: %q", lines)
	}
}
