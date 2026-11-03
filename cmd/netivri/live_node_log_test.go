package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

func TestLiveNodeLoggerBuilderDoesNotReplaceActiveLogger(t *testing.T) {
	l := newLiveAngel(upOptions{}, func(string, ...any) {}, func() {})
	old := makeNodeHealthLogger([]stats.NodeInfo{{Label: "p/old", Country: "FI"}}, nil, nil, nil, nil, nil, time.Now)
	l.installNodeLogger(old)
	d := testDesired()
	d.pool.infos = []stats.NodeInfo{{Label: "p/new", Country: "US"}}
	o, candidate := l.routeOptionsWithLogger(d, nil, nil)
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
	o, candidate = l.routeOptionsWithLogger(d, nil, nil)
	if candidate != nil || o.OnNodeHealth != nil || l.activeNodeLogger() != old {
		t.Fatal("empty candidate changed active logger or installed a hook")
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
