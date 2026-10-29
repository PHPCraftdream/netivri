package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// burstFixture is a node log on a fake clock that collects its lines as
// "WARN ..." and "INFO ...".
type burstFixture struct {
	log   *nodeLog
	now   time.Time
	lines []string
}

func newBurstFixture(t *testing.T, nodes int) *burstFixture {
	t.Helper()
	f := &burstFixture{now: time.Unix(10_000, 0)}
	var infos []stats.NodeInfo
	for i := 0; i < nodes; i++ {
		infos = append(infos, stats.NodeInfo{Label: fmt.Sprintf("p/n%d", i), Provider: "p", Country: "DE"})
	}
	f.log = newNodeLog(infos,
		func(format string, args ...any) { f.lines = append(f.lines, "INFO "+fmt.Sprintf(format, args...)) },
		func(format string, args ...any) { f.lines = append(f.lines, "WARN "+fmt.Sprintf(format, args...)) },
		func() time.Time { return f.now })
	f.now = f.now.Add(10 * time.Minute) // the angel has been up for a while
	return f
}

func (f *burstFixture) down(i int, reason string) {
	f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, Reason: reason})
}

func (f *burstFixture) back(i int, downtime time.Duration) {
	f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Downtime: downtime})
}

func (f *burstFixture) count(prefix string) int {
	n := 0
	for _, l := range f.lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (f *burstFixture) countContaining(text string) int {
	n := 0
	for _, l := range f.lines {
		if strings.Contains(l, text) {
			n++
		}
	}
	return n
}

func TestNodeLogIsolatedFailuresStayIndividualWarnings(t *testing.T) {
	f := newBurstFixture(t, 6)
	f.down(0, "2 probe failures (timeout)")
	f.now = f.now.Add(time.Minute)
	f.down(1, "2 probe failures (timeout)")
	f.now = f.now.Add(time.Minute)
	f.down(2, "2 probe failures (timeout)")
	if f.count("WARN node p/n") != 3 || f.count("WARN node burst") != 0 {
		t.Fatalf("lines = %q, want three separate warnings and no summary", f.lines)
	}
}

func TestNodeLogBurstIsOneWarningAndTheRestInfo(t *testing.T) {
	f := newBurstFixture(t, 12)
	for i := 0; i < 8; i++ {
		f.down(i, "2 probe failures (timeout)")
		f.now = f.now.Add(500 * time.Millisecond)
	}
	var warns []string
	for _, l := range f.lines {
		if strings.HasPrefix(l, "WARN ") {
			warns = append(warns, l)
		}
	}
	want := []string{
		"WARN node p/n0 (provider p, country DE) is down: 2 probe failures (timeout)",
		"WARN node p/n1 (provider p, country DE) is down: 2 probe failures (timeout)",
		"WARN node burst: 3 of 12 nodes went down within 10s (probe failures (timeout) x3), likely a lost network or a shared outage",
	}
	if !reflect.DeepEqual(warns, want) {
		t.Fatalf("warnings = %q\nwant %q", warns, want)
	}
	if got := f.count("INFO node p/n"); got != 6 { // the third node and the five after it
		t.Errorf("%d info lines for the nodes of the burst, want 6: %q", got, f.lines)
	}
}

func TestNodeLogBurstNamesMixedReasonsAndTheStart(t *testing.T) {
	f := newBurstFixture(t, 5)
	f.now = f.log.started.Add(20 * time.Second)
	f.down(0, "2 probe failures (timeout)")
	f.down(1, "2 probe failures (connection refused)")
	f.down(2, "2 probe failures (timeout)")
	var summary string
	for _, l := range f.lines {
		if strings.HasPrefix(l, "WARN node burst") {
			summary = l
		}
	}
	want := "WARN node burst: 3 of 5 nodes went down within 10s (probe failures (connection refused) x1, probe failures (timeout) x2), likely a lost network or a shared outage, right after start"
	if summary != want {
		t.Errorf("summary = %q\nwant %q", summary, want)
	}
}

func TestNodeLogSlowFailuresDoNotMakeABurst(t *testing.T) {
	f := newBurstFixture(t, 6)
	for i := 0; i < 5; i++ {
		f.down(i, "2 probe failures (timeout)")
		f.now = f.now.Add(burstWindow + time.Second)
	}
	if f.count("WARN node burst") != 0 || f.count("WARN node p/n") != 5 {
		t.Fatalf("lines = %q, want five separate warnings", f.lines)
	}
}

func TestNodeLogClosesTheBurstWhenTheLastNodeIsBack(t *testing.T) {
	f := newBurstFixture(t, 6)
	for i := 0; i < 4; i++ {
		f.down(i, "2 probe failures (timeout)")
	}
	f.now = f.now.Add(41 * time.Second)
	for i := 0; i < 3; i++ {
		f.back(i, 41*time.Second)
	}
	if f.count("INFO node burst over") != 0 {
		t.Fatalf("the burst closed with a node still down: %q", f.lines)
	}
	f.back(3, 41*time.Second)
	last := f.lines[len(f.lines)-1]
	if last != "INFO node burst over: 4 nodes were down, all back after 41s" {
		t.Errorf("last line = %q", last)
	}
	if got := f.lines[len(f.lines)-2]; got != "INFO node p/n3 (provider p, country DE) is back after 41s down" {
		t.Errorf("back line = %q", got)
	}
	// A later outage of one node is not part of the old burst.
	f.now = f.now.Add(time.Hour)
	f.down(5, "3 dial failures")
	f.back(5, 7*time.Second)
	if f.count("INFO node burst over") != 1 {
		t.Errorf("a single node closed a burst: %q", f.lines)
	}
}

func TestNodeLogFlappingNodeIsOneWarning(t *testing.T) {
	f := newBurstFixture(t, 4)
	for i := 0; i < 5; i++ {
		f.down(0, "2 probe failures (timeout)")
		f.now = f.now.Add(40 * time.Second)
		f.back(0, 40*time.Second)
		f.now = f.now.Add(3 * time.Minute)
	}
	var warns []string
	for _, l := range f.lines {
		if strings.HasPrefix(l, "WARN ") {
			warns = append(warns, l)
		}
	}
	want := []string{
		"WARN node p/n0 (provider p, country DE) is down: 2 probe failures (timeout)",
		"WARN node p/n0 (provider p, country DE) is down: 2 probe failures (timeout)",
		"WARN node p/n0 (provider p, country DE) is flapping: 3 downs within the last hour (probe failures (timeout) x3); its further down and back reports are INFO",
	}
	if !reflect.DeepEqual(warns, want) {
		t.Fatalf("warnings = %q\nwant %q", warns, want)
	}
	if got := f.count("INFO node p/n0"); got != 5+3 {
		t.Logf("lines = %q", f.lines)
		t.Errorf("%d info lines, want 8 (5 back, 3 down)", got)
	}
}

func TestNodeLogFlappingNodesDoNotMakeABurst(t *testing.T) {
	f := newBurstFixture(t, 6)
	for round := 0; round < 3; round++ {
		for i := 0; i < 3; i++ {
			f.down(i, "2 probe failures (timeout)")
		}
		f.now = f.now.Add(time.Minute)
		for i := 0; i < 3; i++ {
			f.back(i, time.Minute)
		}
		f.now = f.now.Add(time.Minute)
	}
	// Rounds one and two raised a burst each; in round three all three nodes
	// are flapping, so there is no burst, only their flapping warnings.
	if got := f.count("WARN node burst"); got != 2 {
		t.Errorf("%d burst warnings, want 2: %q", got, f.lines)
	}
	if got := f.countContaining("is flapping"); got != 3 {
		t.Errorf("%d flapping warnings, want 3: %q", got, f.lines)
	}
}

func TestNodeLogForgetsOldDowns(t *testing.T) {
	f := newBurstFixture(t, 3)
	for i := 0; i < 2; i++ {
		f.down(0, "2 probe failures (timeout)")
		f.now = f.now.Add(flapWindow/2 + time.Minute)
	}
	f.down(0, "2 probe failures (timeout)") // the first drop is older than the window
	if got := f.countContaining("is flapping"); got != 0 {
		t.Errorf("flapping after two recent downs: %q", f.lines)
	}
	if got := f.count("WARN node p/n0"); got != 3 {
		t.Errorf("%d warnings, want 3", got)
	}
}

func TestNodeLogNeverAnsweredNodesAreInfoAndOutsideTheBurst(t *testing.T) {
	f := newBurstFixture(t, 10)
	for i := 0; i < 8; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, Reason: "2 probe failures (timeout)", NeverOK: true})
	}
	if got := f.count("WARN"); got != 0 {
		t.Fatalf("%d warnings for nodes that never answered: %q", got, f.lines)
	}
	if got := f.count("INFO node p/n"); got != 8 {
		t.Fatalf("%d info lines, want 8: %q", got, f.lines)
	}
	want := "INFO node p/n0 (provider p, country DE) has never answered: 2 probe failures (timeout)"
	if f.lines[0] != want {
		t.Errorf("line = %q, want %q", f.lines[0], want)
	}
	// They are not part of a burst either: two real downs stay two warnings.
	f.down(8, "2 probe failures (timeout)")
	f.down(9, "2 probe failures (timeout)")
	if got := f.count("WARN node p/n"); got != 2 || f.count("WARN node burst") != 0 {
		t.Errorf("lines = %q, want two plain warnings and no burst", f.lines)
	}
	// And they do not count toward flapping: a node that never answered and
	// then went down and up again is still new to the flap count.
	f.back(0, time.Minute)
	if got := f.count("INFO node p/n0 (provider p, country DE) is back after 1m0s down"); got != 1 {
		t.Errorf("back line missing: %q", f.lines)
	}
}

func TestNodeLogWithoutNodesHasNoHook(t *testing.T) {
	if newNodeLog(nil, nil, nil, time.Now) != nil {
		t.Fatal("a node log for a route without nodes")
	}
}

func TestIsNodeDownMessageCoversTheNewForms(t *testing.T) {
	cases := map[string]bool{
		"node main/a (FI, provider main, country FI) is down: 2 probe failures (timeout), last ok 3m0s ago":                      true,
		"node main/a (provider main, country FI) is down: 3 dial failures":                                                       true,
		"node burst: 3 of 36 nodes went down within 10s (probe failures (timeout) x3), likely a lost network or a shared outage": true,
		"node main/a (provider main, country FI) is back after 41s down":                                                         false,
		"subscription main: refresh failed: is down: x":                                                                          false,
		"the node is down: no": false,
	}
	for message, want := range cases {
		if got := isNodeDownMessage(message); got != want {
			t.Errorf("isNodeDownMessage(%q) = %v, want %v", message, got, want)
		}
	}
}

// twinFixture is a node log over servers that two providers list: own/a and
// main/a are one server, own/b and main/b another, own/c is alone.
func twinFixture(t *testing.T) *burstFixture {
	t.Helper()
	f := &burstFixture{now: time.Unix(10_000, 0)}
	var infos []stats.NodeInfo
	for _, label := range []string{"own/a", "main/a", "own/b", "main/b", "own/c", "own/d"} {
		provider, _, _ := strings.Cut(label, "/")
		infos = append(infos, stats.NodeInfo{Label: label, Provider: provider, Country: "FI"})
	}
	f.log = newNodeLog(infos,
		func(format string, args ...any) { f.lines = append(f.lines, "INFO "+fmt.Sprintf(format, args...)) },
		func(format string, args ...any) { f.lines = append(f.lines, "WARN "+fmt.Sprintf(format, args...)) },
		func() time.Time { return f.now })
	f.now = f.now.Add(10 * time.Minute)
	return f
}

func (f *burstFixture) downLabel(label string) {
	f.log.event(core.NodeEvent{Label: label, Down: true, Reason: "2 probe failures (timeout)"})
}

func TestNodeLogTwinOfTheSameServerIsOneEvent(t *testing.T) {
	f := twinFixture(t)
	f.downLabel("own/a")
	f.now = f.now.Add(2 * time.Second)
	f.downLabel("main/a")
	if got := f.count("WARN"); got != 1 {
		t.Errorf("%d warnings for one server, want 1: %q", got, f.lines)
	}
	if got := f.countContaining("is down: 2 probe failures (timeout), same server as own/a"); got != 1 {
		t.Errorf("twin line missing: %q", f.lines)
	}
	if !isNodeDownMessage(strings.TrimPrefix(f.lines[1], "INFO ")) {
		t.Errorf("doctor does not recognise the twin line: %q", f.lines[1])
	}
}

func TestNodeLogTwinsAreOneServerForBurstAndFlapping(t *testing.T) {
	f := twinFixture(t)
	// Two servers (four labels) down together are not three nodes: no burst.
	for _, label := range []string{"own/a", "main/a", "own/b", "main/b"} {
		f.downLabel(label)
	}
	if got := f.count("WARN node burst"); got != 0 {
		t.Errorf("burst from two servers: %q", f.lines)
	}
	// A third server makes it a burst of three.
	f.downLabel("own/c")
	if got := f.count("WARN node burst: 3 of"); got != 1 {
		t.Errorf("no burst of three servers: %q", f.lines)
	}

	// The same server dropping three times, reported by both labels each
	// time, flaps once, after its third drop and not its second.
	g := twinFixture(t)
	for i := 0; i < 3; i++ {
		g.downLabel("own/a")
		g.downLabel("main/a")
		g.now = g.now.Add(3 * time.Minute)
	}
	if got := g.countContaining("is flapping: 3 downs"); got != 1 {
		t.Errorf("%d flapping warnings, want 1: %q", got, g.lines)
	}
	if got := g.countContaining("flapping: 2 downs"); got != 0 {
		t.Errorf("flapping after two drops: %q", g.lines)
	}
}

func TestNodeLogTwinOutsideTheWindowIsItsOwnEvent(t *testing.T) {
	f := twinFixture(t)
	f.downLabel("own/a")
	f.now = f.now.Add(twinWindow + time.Second)
	f.downLabel("main/a")
	if got := f.count("WARN"); got != 2 {
		t.Errorf("%d warnings, want 2 (the twin came late): %q", got, f.lines)
	}
	if got := f.countContaining("same server as"); got != 0 {
		t.Errorf("late twin got the same-server note: %q", f.lines)
	}
}

func TestServerID(t *testing.T) {
	for in, want := range map[string]string{"own/abc": "abc", "main/abc": "abc", "plain": "plain", "x/": "x/"} {
		if got := serverID(in); got != want {
			t.Errorf("serverID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNodeLogNeverAnsweredTwinIsNamed(t *testing.T) {
	f := twinFixture(t)
	f.log.event(core.NodeEvent{Label: "own/a", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	f.log.event(core.NodeEvent{Label: "main/a", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	f.log.event(core.NodeEvent{Label: "own/c", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	if got := f.countContaining("same server as own/a"); got != 1 {
		t.Errorf("%d lines name the twin, want 1: %q", got, f.lines)
	}
	if got := f.countContaining("has never answered"); got != 3 {
		t.Errorf("%d never-answered lines, want 3: %q", got, f.lines)
	}
	// A never-answered report is not a down of its twin.
	f.downLabel("main/a")
	if got := f.countContaining("is down: 2 probe failures (timeout), same server"); got != 0 {
		t.Errorf("a down was taken for the twin of a never-answered node: %q", f.lines)
	}
}

func TestNodeLogOnlyFirstTierNodesWarn(t *testing.T) {
	f := newBurstFixture(t, 4)
	f.log.first = map[string]bool{"p/n0": true}
	f.down(0, "2 probe failures (timeout)")
	f.down(1, "2 probe failures (timeout)")
	if got := f.count("WARN node p/n0"); got != 1 {
		t.Errorf("first-tier node: %d warnings, want 1: %q", got, f.lines)
	}
	if got := f.count("INFO node p/n1"); got != 1 || f.count("WARN node p/n1") != 0 {
		t.Errorf("fallback node must be INFO: %q", f.lines)
	}
	// Flapping of a fallback node is INFO too.
	for i := 0; i < 3; i++ {
		f.now = f.now.Add(time.Minute)
		f.down(2, "2 probe failures (timeout)")
	}
	if got := f.count("WARN node p/n2"); got != 0 {
		t.Errorf("fallback node warned: %q", f.lines)
	}
	if got := f.countContaining("p/n2 (provider p, country DE) is flapping"); got != 1 {
		t.Errorf("flapping line of the fallback node missing: %q", f.lines)
	}
	// With no tier information every node still warns.
	g := newBurstFixture(t, 2)
	g.down(1, "2 probe failures (timeout)")
	if got := g.count("WARN node p/n1"); got != 1 {
		t.Errorf("nil first-tier set: %q", g.lines)
	}
}

func TestFirstTierLabels(t *testing.T) {
	pool := routePool{
		labels: map[string][]string{
			"k1": {"own/a", "main/b", "own/c"},
			"k2": {"own/c", "main/d"},
			"k3": {},
		},
		tiers: map[string][]int{
			"k1": {0, 0, 1},
			"k2": {2, 3}, // the lowest tier of a route is its first one
			"k3": {},
		},
	}
	got := pool.firstTierLabels()
	want := map[string]bool{"own/a": true, "main/b": true, "own/c": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("firstTierLabels = %v, want %v", got, want)
	}
}
