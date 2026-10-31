package main

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/core"
)

// A route with a pre or post hop says where the failed dials came from: one line
// per account, node and stage, so a refusing post hop is not mistaken for a node.
func TestConnLogNamesTheStageOfFailedDials(t *testing.T) {
	f := newConnLogFixture()
	dial := func(hop, why string, n int) {
		for i := 0; i < n; i++ {
			f.log.event(core.ConnEvent{Kind: core.ConnDialFailed, Login: "alice", Label: "main/us", Why: why, Hop: hop})
		}
	}
	dial(core.HopPost, "proxy auth failed", 3)
	dial(core.HopNode, "timeout", 1)
	dial("", "timeout", 2) // a route with no hop: the line is as it always was
	f.log.flush()
	want := []string{
		"INFO account alice via main/us (United States, country US): 2 dials failed (timeout x2)",
		"INFO account alice via main/us (United States, country US), node: 1 dial failed (timeout x1)",
		"WARN account alice via main/us (United States, country US), post hop: 3 dials failed (proxy auth failed x3)",
	}
	if strings.Join(f.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(f.lines, "\n"), strings.Join(want, "\n"))
	}
	f.lines = nil
	f.log.heartbeat()
	if len(f.lines) != 1 || !strings.Contains(f.lines[0], "6 dials failed") {
		t.Errorf("the hourly summary counts every stage together: %q", f.lines)
	}
}
