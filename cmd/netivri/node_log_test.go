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

func TestNodeLogNeverAnsweredCollapsesUniqueServers(t *testing.T) {
	now := time.Unix(10000, 0)
	var infoLines, debugLines []string
	labels := []string{"p/s1", "q/s1", "p/s2", "q/s2", "p/s3", "q/s3", "p/s4", "q/s4", "p/s5"}
	countries := []string{"FI", "FI", "US", "US", "DE", "DE", "NL", "NL", "SE"}
	infos := make([]stats.NodeInfo, 0, len(labels))
	for i, label := range labels {
		infos = append(infos, stats.NodeInfo{Label: label, Provider: label[:1], Country: countries[i]})
	}
	l := makeNodeHealthLogger(infos, nil, nil,
		func(format string, args ...any) { debugLines = append(debugLines, fmt.Sprintf(format, args...)) },
		func(format string, args ...any) { infoLines = append(infoLines, fmt.Sprintf(format, args...)) }, nil, func() time.Time { return now })
	for _, label := range labels {
		l.event(core.NodeEvent{Label: label, Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	}
	wantInfo := []string{
		"node p/s1 (provider p, country FI) has never answered: 2 probe failures (timeout)",
		"node q/s1 (provider q, country FI) has never answered: 2 probe failures (timeout), same server as p/s1",
		"node p/s2 (provider p, country US) has never answered: 2 probe failures (timeout)",
		"node q/s2 (provider q, country US) has never answered: 2 probe failures (timeout), same server as p/s2",
		"3 servers have never answered since start (probe failures (timeout) x3; DE x1, FI x1, US x1); probed rarely, one DEBUG line each",
	}
	wantDebug := []string{
		"node p/s3 (provider p, country DE) has never answered: 2 probe failures (timeout)",
		"node q/s3 (provider q, country DE) has never answered: 2 probe failures (timeout), same server as p/s3",
		"node p/s4 (provider p, country NL) has never answered: 2 probe failures (timeout)",
		"node q/s4 (provider q, country NL) has never answered: 2 probe failures (timeout), same server as p/s4",
		"node p/s5 (provider p, country SE) has never answered: 2 probe failures (timeout)",
	}
	if !reflect.DeepEqual(infoLines, wantInfo) {
		t.Fatalf("INFO lines = %q, want %q", infoLines, wantInfo)
	}
	if !reflect.DeepEqual(debugLines, wantDebug) {
		t.Fatalf("DEBUG lines = %q, want %q", debugLines, wantDebug)
	}
	l.event(core.NodeEvent{Label: "p/s3", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	wantDebug = append(wantDebug, "node p/s3 (provider p, country DE) has never answered: 2 probe failures (timeout)")
	if !reflect.DeepEqual(infoLines, wantInfo) || !reflect.DeepEqual(debugLines, wantDebug) {
		t.Fatalf("repeat of summarized server changed logs: INFO=%q DEBUG=%q", infoLines, debugLines)
	}
	now = now.Add(neverWindow)
	l.flush()
	wantInfo = append(wantInfo, "5 servers have never answered since start (probe failures (timeout) x5; DE x1, FI x1, NL x1, SE x1, US x1); probed rarely, one DEBUG line each")
	if !reflect.DeepEqual(infoLines, wantInfo) {
		t.Fatalf("INFO after flush = %q, want %q", infoLines, wantInfo)
	}
	beforeDebug := append([]string(nil), debugLines...)
	l.event(core.NodeEvent{Label: "p/s3", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	wantInfo = append(wantInfo, "node p/s3 (provider p, country DE) has never answered: 2 probe failures (timeout)")
	if !reflect.DeepEqual(debugLines, wantDebug) || !reflect.DeepEqual(infoLines, wantInfo) {
		t.Fatalf("post-expiry repeat level changed incorrectly: INFO=%q DEBUG=%q (before DEBUG %q)", infoLines, debugLines, beforeDebug)
	}
	l.flushNow()
	if !reflect.DeepEqual(infoLines, wantInfo) {
		t.Fatalf("forced flush duplicated summary: %q", infoLines)
	}
}

func TestNodeLogNeverAnsweredMixedKindsAndUnknownCountry(t *testing.T) {
	now := time.Unix(10000, 0)
	var lines []string
	infos := []stats.NodeInfo{{Label: "p/a", Country: ""}, {Label: "p/b", Country: "US"}, {Label: "p/c", Country: "DE"}}
	l := makeNodeHealthLogger(infos, nil, nil, func(format string, args ...any) { lines = append(lines, "DEBUG "+fmt.Sprintf(format, args...)) }, func(format string, args ...any) { lines = append(lines, "INFO "+fmt.Sprintf(format, args...)) }, nil, func() time.Time { return now })
	for _, ev := range []core.NodeEvent{{Label: "p/a", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"}, {Label: "p/b", Down: true, NeverOK: true, Reason: "3 probe failures (connection refused)"}, {Label: "p/c", Down: true, NeverOK: true, Reason: "4 dial failures (timeout)"}} {
		l.event(ev)
	}
	want := "INFO 3 servers have never answered since start (dial failures (timeout) x1, probe failures (connection refused) x1, probe failures (timeout) x1; DE x1, US x1, unknown x1); probed rarely, one DEBUG line each"
	if lines[2] != want {
		t.Fatalf("mixed/unknown summary = %q, want %q", lines[2], want)
	}
}

func TestNodeLogNeverAnsweredForceFlushSummary(t *testing.T) {
	f := newBurstFixture(t, 5)
	for i := 0; i < 5; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	before := f.count("INFO 5 servers have never answered")
	f.log.flushNow()
	if before != 0 || f.count("INFO 5 servers have never answered") != 1 {
		t.Fatalf("forced summary flush: %q", f.lines)
	}
	f.log.flushNow()
	if f.count("INFO 5 servers have never answered") != 1 {
		t.Fatalf("forced summary repeated: %q", f.lines)
	}
}

func TestNodeLogNeverAnsweredWindowExpiresOnEvent(t *testing.T) {
	f := newBurstFixture(t, 4)
	for i := 0; i < 3; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	f.now = f.now.Add(neverWindow)
	f.log.event(core.NodeEvent{Label: "p/n3", Down: true, NeverOK: true, Reason: "timeout"})
	if f.count("INFO 3 servers have never answered since start") != 1 || f.count("INFO node p/n3") != 1 || f.count("DEBUG node p/n3") != 0 {
		t.Fatalf("expired event handling: %q", f.lines)
	}
}

func TestNodeLogNeverAnsweredFirstTwoStayIndividual(t *testing.T) {
	f := newBurstFixture(t, 4)
	for i := 0; i < 2; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	before := append([]string(nil), f.lines...)
	first := "INFO node p/n0 (provider p, country DE) has never answered: timeout"
	second := "INFO node p/n1 (provider p, country DE) has never answered: timeout"
	if !reflect.DeepEqual(before, []string{first, second}) {
		t.Fatalf("first two lines = %q", before)
	}
	f.now = f.now.Add(neverWindow)
	f.log.flush()
	if !reflect.DeepEqual(f.lines, before) {
		t.Fatalf("two-node expiry wrote output: %q", f.lines)
	}
}

func TestNodeLogNeverAnsweredFlushWithoutGrowthDoesNothing(t *testing.T) {
	f := newBurstFixture(t, 4)
	for i := 0; i < 3; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	if f.count("INFO 3 servers have never answered since start") != 1 {
		t.Fatalf("initial third-server summary missing: %q", f.lines)
	}
	f.now = f.now.Add(neverWindow)
	before := len(f.lines)
	f.log.flush()
	if len(f.lines) != before || f.count("INFO 3 servers have never answered since start") != 1 {
		t.Fatalf("final flush without growth wrote output: %q", f.lines)
	}
	f.log.flush()
	if len(f.lines) != before {
		t.Fatalf("repeated flush wrote output: %q", f.lines)
	}
}

func TestNodeLogNeverAnsweredNilFlush(t *testing.T) { var l *nodeLog; l.flush() }
func TestNodeLogNeverAnsweredRecoveryIsInfo(t *testing.T) {
	f := newBurstFixture(t, 4).withDebug()
	for i := 0; i < 3; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	f.back(2, time.Minute)
	if f.count("INFO node p/n2 (provider p, country DE) is back after 1m0s down") != 1 {
		t.Fatalf("recovery line: %q", f.lines)
	}
}

func TestNodeLogNeverAnsweredDoesNotChangeOutageAccounting(t *testing.T) {
	f := newBurstFixture(t, 12).withDebug()
	for i := 6; i < 12; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	f.slowly(0, 5)
	if f.countContaining("node outage") != 0 {
		t.Fatalf("never nodes counted as answered/down: %q", f.lines)
	}
	f.down(5, "timeout")
	if f.count("WARN node outage: 6 of 6 servers that have answered are down") != 1 {
		t.Fatalf("outage accounting changed: %q", f.lines)
	}
}
func TestNodeLogNeverAnsweredNodesAreInfoAndOutsideTheBurst(t *testing.T) {
	f := newBurstFixture(t, 10).withDebug()
	for i := 0; i < 8; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, Reason: "2 probe failures (timeout)", NeverOK: true})
	}
	if f.count("INFO node p/n") != 2 || f.count("INFO ") != 3 || f.count("DEBUG node p/") != 6 || f.count("WARN") != 0 {
		t.Fatalf("never answered levels: %q", f.lines)
	}
	f.down(8, "2 probe failures (timeout)")
	f.down(9, "2 probe failures (timeout)")
	if f.count("WARN node p/n") != 2 || f.count("WARN node burst") != 0 {
		t.Fatalf("never reports affected down accounting: %q", f.lines)
	}
	f.back(0, time.Minute)
	if f.countContaining("is back after 1m0s down") != 1 {
		t.Fatalf("recovery missing: %q", f.lines)
	}
}

func TestNodeLogWithoutNodesHasNoHook(t *testing.T) {
	if newNodeLog(nil, nil, nil, time.Now) != nil {
		t.Fatal("a node log for a route without nodes")
	}
}

func TestIsNodeDownMessageCoversTheNewForms(t *testing.T) {
	cases := map[string]bool{
		"node main/a (FI, provider main, country FI) is down: 2 probe failures (timeout), last ok 3m0s ago":                                                           true,
		"node main/a (provider main, country FI) is down: 3 dial failures":                                                                                            true,
		"node burst: 3 of 36 nodes went down within 10s (probe failures (timeout) x3), likely a lost network or a shared outage":                                      true,
		"node outage: 11 of 21 servers that have answered are down, likely a lost network or the provider; their down and back reports are DEBUG until most are back": true,
		"node outage over: up to 14 of 21 servers were down, 2 still down, after 6h12m3s":                                                                             false,
		"node main/a (provider main, country FI) is back after 41s down":                                                                                              false,
		"subscription main: refresh failed: is down: x":                                                                                                               false,
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

func TestNodeLogNeverAnsweredTwinsStayAnnotatedInfo(t *testing.T) {
	f := twinFixture(t)
	f.log.event(core.NodeEvent{Label: "own/a", Down: true, NeverOK: true, Reason: "timeout"})
	f.log.event(core.NodeEvent{Label: "main/a", Down: true, NeverOK: true, Reason: "timeout"})
	if len(f.lines) != 2 || !strings.Contains(f.lines[1], "same server as own/a") || !strings.HasPrefix(f.lines[1], "INFO ") {
		t.Fatalf("twin line: %q", f.lines)
	}
}
func TestNodeLogNeverAnsweredTwinIsNamed(t *testing.T) {
	f := twinFixture(t)
	f.log.event(core.NodeEvent{Label: "own/a", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	f.log.event(core.NodeEvent{Label: "main/a", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	f.log.event(core.NodeEvent{Label: "own/c", Down: true, NeverOK: true, Reason: "2 probe failures (timeout)"})
	if got := f.countContaining("has never answered"); got != 3 {
		t.Errorf("never-answered lines including twin and third server = %d, want 3: %q", got, f.lines)
	}
	if len(f.lines) < 2 || !strings.Contains(f.lines[1], "same server as own/a") || !strings.HasPrefix(f.lines[1], "INFO ") {
		t.Fatalf("twin line: %q", f.lines)
	}
	f.downLabel("main/a")
	if f.countContaining("is down: 2 probe failures (timeout), same server") != 0 {
		t.Errorf("never event changed down twin state: %q", f.lines)
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

// debugFixture is a twin fixture that also collects DEBUG lines, with own/a
// the only first-tier node.
func debugFixture(t *testing.T) *burstFixture {
	t.Helper()
	f := twinFixture(t)
	f.log.first = map[string]bool{"own/a": true, "main/a": true}
	f.log.debug = func(format string, args ...any) { f.lines = append(f.lines, "DEBUG "+fmt.Sprintf(format, args...)) }
	return f
}

func TestNodeLogRepeatsOfFlappingFallbackNodesAreDebug(t *testing.T) {
	f := debugFixture(t)
	// own/c is a fallback node: three downs make it flap, the rest is DEBUG.
	for i := 0; i < 5; i++ {
		f.downLabel("own/c")
		f.now = f.now.Add(30 * time.Second)
		f.log.event(core.NodeEvent{Label: "own/c", Downtime: 30 * time.Second})
		f.now = f.now.Add(3 * time.Minute)
	}
	if got := f.countContaining("own/c (provider own, country FI) is flapping"); got != 1 {
		t.Errorf("%d flapping lines, want 1: %q", got, f.lines)
	}
	if got := f.count("WARN"); got != 0 {
		t.Errorf("a fallback node warned: %q", f.lines)
	}
	if got := f.count("DEBUG node own/c"); got != 3+3 {
		t.Errorf("%d DEBUG lines, want 3 downs and 3 backs after the flapping notice: %q", got, f.lines)
	}
	// Two downs and two backs before it flapped were INFO.
	if got := f.count("INFO node own/c"); got != 2+2+1 {
		t.Errorf("%d INFO lines, want 2 downs, 2 backs and the flapping notice: %q", got, f.lines)
	}
}

func TestNodeLogFirstTierRepeatsStayInfo(t *testing.T) {
	f := debugFixture(t)
	for i := 0; i < 5; i++ {
		f.downLabel("own/a")
		f.now = f.now.Add(30 * time.Second)
		f.log.event(core.NodeEvent{Label: "own/a", Downtime: 30 * time.Second})
		f.now = f.now.Add(3 * time.Minute)
	}
	if got := f.count("DEBUG"); got != 0 {
		t.Errorf("a first-tier node wrote DEBUG lines: %q", f.lines)
	}
}

func TestNodeLogTwinOfAFallbackNodeIsDebug(t *testing.T) {
	f := debugFixture(t)
	f.downLabel("own/b")
	f.downLabel("main/b")
	if got := f.countContaining("same server as own/b"); got != 1 || f.count("DEBUG node main/b") != 1 {
		t.Errorf("twin of a fallback node must be one DEBUG line: %q", f.lines)
	}
	// The twin of a first-tier node stays INFO.
	f.downLabel("own/a")
	f.downLabel("main/a")
	if f.count("INFO node main/a") != 1 || f.count("DEBUG node main/a") != 0 {
		t.Errorf("twin of a first-tier node must be INFO: %q", f.lines)
	}
}

func TestNodeLogWithoutADebugWriterRepeatsAreInfo(t *testing.T) {
	f := twinFixture(t)
	f.log.first = map[string]bool{"own/a": true}
	f.downLabel("own/b")
	f.downLabel("main/b")
	if got := f.count("INFO node main/b"); got != 1 {
		t.Errorf("without a debug writer the twin line must stay INFO: %q", f.lines)
	}
}

func TestNodeLogTwinBackIsOneEvent(t *testing.T) {
	f := twinFixture(t)
	f.downLabel("own/a")
	f.now = f.now.Add(time.Minute)
	f.log.event(core.NodeEvent{Label: "own/a", Downtime: time.Minute})
	f.now = f.now.Add(time.Second)
	f.log.event(core.NodeEvent{Label: "main/a", Downtime: time.Minute})
	if got := f.countContaining("own/a (provider own, country FI) is back after 1m0s down"); got != 1 {
		t.Errorf("%d back lines for own/a, want 1: %q", got, f.lines)
	}
	if got := f.countContaining("main/a (provider main, country FI) is back after 1m0s down, same server as own/a"); got != 1 {
		t.Errorf("twin back line has no note: %q", f.lines)
	}
	// Late twin, or another server, is its own event.
	f.now = f.now.Add(twinWindow + time.Second)
	f.log.event(core.NodeEvent{Label: "own/a", Downtime: time.Minute})
	f.log.event(core.NodeEvent{Label: "own/b", Downtime: time.Minute})
	if got := f.countContaining("same server as"); got != 1 {
		t.Errorf("a later back or another server got the note: %q", f.lines)
	}
}

func TestNodeLogTwinBackOfFallbackNodeIsDebug(t *testing.T) {
	f := twinFixture(t)
	var debug []string
	f.log.debug = func(format string, args ...any) { debug = append(debug, fmt.Sprintf(format, args...)) }
	f.log.first = map[string]bool{}
	f.log.event(core.NodeEvent{Label: "own/a", Downtime: time.Minute})
	f.log.event(core.NodeEvent{Label: "main/a", Downtime: time.Minute})
	if f.countContaining("is back") != 1 || len(debug) != 1 || !strings.Contains(debug[0], "same server as own/a") {
		t.Errorf("info = %q, debug = %q", f.lines, debug)
	}
}

// withDebug makes the fixture collect DEBUG lines as "DEBUG ...".
func (f *burstFixture) withDebug() *burstFixture {
	f.log.debug = func(format string, args ...any) { f.lines = append(f.lines, "DEBUG "+fmt.Sprintf(format, args...)) }
	return f
}

// minutes of calm between two downs: further apart than the burst window, so
// the downs are never a burst.
func (f *burstFixture) slowly(from, to int) {
	for i := from; i < to; i++ {
		f.down(i, "2 probe failures (timeout)")
		f.now = f.now.Add(time.Minute)
	}
}

func TestNodeLogSlowOutageIsOneWarningAndTheRestDebug(t *testing.T) {
	f := newBurstFixture(t, 12).withDebug()
	f.slowly(0, 5)
	if f.count("WARN node p/n") != 5 || f.countContaining("node outage") != 0 {
		t.Fatalf("five of twelve are separate warnings, no outage yet: %q", f.lines)
	}
	f.slowly(5, 9)
	if got := f.count("WARN node outage: 6 of 12 servers that have answered are down"); got != 1 {
		t.Fatalf("%d outage warnings, want 1: %q", got, f.lines)
	}
	if got := f.count("WARN"); got != 5+1 {
		t.Errorf("%d warnings, want the five before and the outage line: %q", got, f.lines)
	}
	if got := f.count("DEBUG node p/n"); got != 4 { // the sixth node and the three after it
		t.Errorf("%d DEBUG down lines, want 4: %q", got, f.lines)
	}
	if got := f.count("INFO"); got != 0 {
		t.Errorf("info lines during the outage: %q", f.lines)
	}

	// The backs are DEBUG; it ends when fewer than a quarter are still down.
	for i := 0; i < 6; i++ {
		f.back(i, time.Hour)
	}
	if f.countContaining("node outage over") != 0 {
		t.Fatalf("the outage ended with three of twelve still down: %q", f.lines)
	}
	f.now = f.now.Add(time.Minute)
	f.back(6, time.Hour)
	last := f.lines[len(f.lines)-1]
	if !strings.HasPrefix(last, "INFO node outage over: up to 9 of 12 servers were down, 2 still down, after ") {
		t.Errorf("last line = %q", last)
	}
	if got := f.count("INFO"); got != 1 {
		t.Errorf("%d info lines, want only the end of the outage: %q", got, f.lines)
	}
	if got := f.count("DEBUG node p/n"); got != 4+7 {
		t.Errorf("%d DEBUG lines, want 4 downs and 7 backs: %q", got, f.lines)
	}

	// A back after the end is an ordinary INFO line again.
	f.back(7, time.Hour)
	if got := f.lines[len(f.lines)-1]; !strings.HasPrefix(got, "INFO node p/n7 ") {
		t.Errorf("back after the outage = %q", got)
	}
	// And a new slow failure of most nodes is a new outage.
	f.slowly(0, 6)
	if got := f.count("WARN node outage:"); got != 2 {
		t.Errorf("%d outage warnings after the second failure, want 2: %q", got, f.lines)
	}
}

func TestNodeLogOutageCountsServersThatHaveAnswered(t *testing.T) {
	// Six of fourteen: not half of the fourteen...
	f := newBurstFixture(t, 14).withDebug()
	f.slowly(0, 6)
	if f.countContaining("node outage") != 0 {
		t.Fatalf("outage with six of fourteen down: %q", f.lines)
	}
	// ... but half of the seven that ever answered.
	g := newBurstFixture(t, 14).withDebug()
	for i := 8; i < 14; i++ {
		g.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, Reason: "2 probe failures (timeout)", NeverOK: true})
	}
	g.slowly(0, 6)
	if got := g.count("WARN node outage: 6 of 8 servers that have answered are down"); got != 1 {
		t.Errorf("%d outage warnings, want 1: %q", got, g.lines)
	}
}

func TestNodeLogOutageCountsServersNotLabels(t *testing.T) {
	var infos []stats.NodeInfo
	for i := 0; i < 8; i++ {
		for _, p := range []string{"own", "main"} {
			infos = append(infos, stats.NodeInfo{Label: fmt.Sprintf("%s/s%d", p, i), Provider: p, Country: "FI"})
		}
	}
	f := &burstFixture{now: time.Unix(10_000, 0)}
	f.log = newNodeLog(infos,
		func(format string, args ...any) { f.lines = append(f.lines, "INFO "+fmt.Sprintf(format, args...)) },
		func(format string, args ...any) { f.lines = append(f.lines, "WARN "+fmt.Sprintf(format, args...)) },
		func() time.Time { return f.now })
	f.log.debug = func(format string, args ...any) { f.lines = append(f.lines, "DEBUG "+fmt.Sprintf(format, args...)) }
	f.now = f.now.Add(10 * time.Minute)
	for i := 0; i < 6; i++ {
		f.downLabel(fmt.Sprintf("own/s%d", i))
		f.now = f.now.Add(twinWindow + time.Second) // the twin comes late: its own event
		f.downLabel(fmt.Sprintf("main/s%d", i))
		f.now = f.now.Add(time.Minute)
		if got := f.count("WARN node outage:"); (i < 5 && got != 0) || (i == 5 && got != 1) {
			t.Fatalf("after %d servers (%d labels) down: %d outage warnings: %q", i+1, 2*(i+1), got, f.lines)
		}
	}
	if !strings.Contains(strings.Join(f.lines, "\n"), "WARN node outage: 6 of 8 servers") {
		t.Errorf("outage line: %q", f.lines)
	}
}

func TestNodeLogBurstInProgressKeepsItsOwnLines(t *testing.T) {
	f := newBurstFixture(t, 12).withDebug()
	for i := 0; i < 3; i++ {
		f.down(i, "2 probe failures (timeout)")
	}
	f.slowly(3, 10)
	if got := f.countContaining("node outage"); got != 0 {
		t.Errorf("an outage inside a burst: %q", f.lines)
	}
	if got := f.count("WARN node burst"); got != 1 {
		t.Errorf("%d burst warnings, want 1: %q", got, f.lines)
	}
	if got := f.count("DEBUG"); got != 0 {
		t.Errorf("a burst wrote DEBUG lines: %q", f.lines)
	}
}

func TestNodeLogOutageDemotesFirstTierRepeats(t *testing.T) {
	f := newBurstFixture(t, 12).withDebug()
	f.log.first = map[string]bool{"p/n0": true}
	f.slowly(1, 8) // seven fallback nodes: an outage after the sixth
	if f.count("WARN node outage:") != 1 {
		t.Fatalf("no outage: %q", f.lines)
	}
	// A first-tier node that is down and flaps during the outage is DEBUG too.
	for i := 0; i < 4; i++ {
		f.down(0, "2 probe failures (timeout)")
		f.now = f.now.Add(time.Minute)
		f.back(0, time.Minute)
	}
	if got := f.count("INFO node p/n0"); got != 0 {
		t.Errorf("%d INFO lines of a first-tier node during the outage, want 0: %q", got, f.lines)
	}
}

func TestNodeLogOutageStateSurvivesRouteRefresh(t *testing.T) {
	f := newBurstFixture(t, 12).withDebug()
	f.slowly(0, 6)
	infos := make([]stats.NodeInfo, 0, len(f.log.byLabel))
	for label, n := range f.log.byLabel {
		if label == "p/n3" {
			n.Label, n.Provider = "q/n3", "q"
		}
		infos = append(infos, n)
	}
	f.log.refresh(infos, nil)
	if !f.log.outage || len(f.log.downNow) != 6 {
		t.Fatalf("outage state after refresh: outage=%v down=%d", f.log.outage, len(f.log.downNow))
	}
	warns := f.count("WARN")
	f.log.event(core.NodeEvent{Label: "p/n6", Down: true, Reason: "timeout"})
	if f.count("WARN") != warns || f.count("DEBUG node p/n6") != 1 {
		t.Fatalf("refreshed outage did not suppress per-node warning: %q", f.lines)
	}
	for i := 0; i < 5; i++ {
		if i == 3 {
			f.log.event(core.NodeEvent{Label: "q/n3", Downtime: time.Minute})
		} else {
			f.back(i, time.Minute)
		}
	}
	if !strings.HasPrefix(f.lines[len(f.lines)-1], "INFO node outage over: up to 7 of 12 servers were down, 2 still down, after ") {
		t.Fatalf("outage closing summary after refresh: %q", f.lines[len(f.lines)-1])
	}
}

func TestNodeLogRefreshClosesPrunedOutageWithoutBackEvent(t *testing.T) {
	f := newBurstFixture(t, 12)
	f.slowly(0, 6)
	if !f.log.outage || len(f.log.downNow) != 6 {
		t.Fatalf("fixture outage: active=%v down=%d", f.log.outage, len(f.log.downNow))
	}

	infos := make([]stats.NodeInfo, 0, 6)
	for i := 6; i < 12; i++ {
		infos = append(infos, f.log.byLabel[fmt.Sprintf("p/n%d", i)])
	}
	f.log.refresh(infos, nil)
	want := "INFO node outage over: up to 6 of 6 servers were down, 0 still down, after 1m0s"
	if got := f.count("INFO node outage over:"); got != 1 || f.lines[len(f.lines)-1] != want {
		t.Fatalf("outage reconciliation after refresh: count=%d last=%q, want %q", got, f.lines[len(f.lines)-1], want)
	}
	f.log.refresh(infos, nil)
	if got := f.count("INFO node outage over:"); got != 1 || f.log.outage {
		t.Fatalf("outage closed more than once or remained active: count=%d active=%v", got, f.log.outage)
	}

	f.now = f.now.Add(burstWindow + time.Second)
	f.down(6, "timeout")
	if got := f.lines[len(f.lines)-1]; got != "WARN node p/n6 (provider p, country DE) is down: timeout" {
		t.Errorf("retained-node failure after outage: %q", got)
	}
}

func TestNodeLogRefreshPrunesNeverNodesBeforeOutageDenominator(t *testing.T) {
	f := newBurstFixture(t, 13)
	for i := 6; i < 13; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	f.slowly(0, 6)
	if !f.log.outage || f.log.live() != 6 {
		t.Fatalf("fixture live/outage: live=%d active=%v", f.log.live(), f.log.outage)
	}
	for i := 0; i < 4; i++ {
		f.back(i, time.Minute)
	}
	if !f.log.outage || f.log.live() != 6 || len(f.log.downNow) != 2 {
		t.Fatalf("outage before refresh: live=%d down=%d active=%v", f.log.live(), len(f.log.downNow), f.log.outage)
	}

	infos := make([]stats.NodeInfo, 0, 13)
	for i := 0; i < 6; i++ {
		infos = append(infos, f.log.byLabel[fmt.Sprintf("p/n%d", i)])
	}
	for i := 6; i < 10; i++ {
		infos = append(infos, f.log.byLabel[fmt.Sprintf("p/n%d", i)])
	}
	for i := 0; i < 3; i++ {
		infos = append(infos, stats.NodeInfo{Label: fmt.Sprintf("q/new%d", i), Provider: "q", Country: "DE"})
	}
	f.log.refresh(infos, nil)
	if len(f.log.never) != 4 || f.log.live() != 9 {
		t.Errorf("never pruning did not update live denominator: never=%d live=%d, want 4 and 9", len(f.log.never), f.log.live())
	}
	if got := f.count("INFO node outage over:"); got != 1 || !strings.HasPrefix(f.lines[len(f.lines)-1], "INFO node outage over: up to 6 of 9 servers were down, 2 still down, after ") || f.log.outage {
		t.Errorf("outage reconciliation after never pruning: count=%d active=%v last=%q", got, f.log.outage, f.lines[len(f.lines)-1])
	}
}

func TestNodeLogRefreshUpdatesMetadataAndTier(t *testing.T) {
	f := newBurstFixture(t, 2)
	f.log.first = map[string]bool{"p/n0": true}
	f.down(0, "timeout")
	f.log.refresh([]stats.NodeInfo{
		{Label: "p/n0", Provider: "new-provider", Country: "NL", Name: "new-name"},
		{Label: "p/n1", Provider: "p", Country: "DE"},
	}, map[string]bool{})
	f.now = f.now.Add(time.Minute)
	f.back(0, time.Minute)
	f.now = f.now.Add(time.Minute)
	f.down(0, "timeout")
	if got := f.lines[len(f.lines)-1]; got != "INFO node p/n0 (new-name, provider new-provider, country NL) is down: timeout" {
		t.Errorf("updated metadata/tier line = %q", got)
	}
}

func TestNodeLogRefreshPrunesRemovedServerState(t *testing.T) {
	f := newBurstFixture(t, 6)
	f.down(0, "timeout")
	f.log.event(core.NodeEvent{Label: "p/n1", Down: true, NeverOK: true, Reason: "timeout"})
	for i := 0; i < 3; i++ {
		f.down(2, "timeout")
		f.back(2, time.Second)
		f.now = f.now.Add(time.Minute)
	}
	f.now = f.now.Add(burstWindow + time.Second)
	for i := 3; i < 6; i++ {
		f.down(i, "timeout")
	}
	if len(f.log.burst) != 3 || !f.log.flapping["n2"] || len(f.log.neverLabels) != 1 {
		t.Fatalf("fixture state: burst=%v flapping=%v never=%v", f.log.burst, f.log.flapping, f.log.neverLabels)
	}
	f.log.refresh([]stats.NodeInfo{
		{Label: "q/n3", Provider: "q", Country: "DE"},
		{Label: "p/n4", Provider: "p", Country: "DE"},
		{Label: "p/n5", Provider: "p", Country: "DE"},
	}, nil)
	for id := range f.log.drops {
		if id == "n0" || id == "n1" || id == "n2" {
			t.Errorf("removed server %s retained flap history", id)
		}
	}
	for _, states := range []map[string]drop{f.log.lastDown, f.log.lastNever, f.log.lastBack} {
		for id := range states {
			if id == "n0" || id == "n1" || id == "n2" {
				t.Errorf("removed server %s retained keyed drop state", id)
			}
		}
	}
	for _, states := range []map[string]struct{}{f.log.downNow, f.log.never, f.log.neverLabels} {
		for id := range states {
			if id == "n0" || id == "n1" || id == "n2" {
				t.Errorf("removed server %s retained keyed state", id)
			}
		}
	}
	lines := len(f.lines)
	f.log.event(core.NodeEvent{Label: "p/n1", Down: true, NeverOK: true, Reason: "timeout"})
	neverPresent := false
	_, neverPresent = f.log.never["n1"]
	if len(f.lines) != lines || neverPresent || f.log.lastNever["n1"].label != "" {
		t.Errorf("late event from removed node changed logger state: %q", f.lines[lines:])
	}
	burstN3 := false
	_, burstN3 = f.log.burst["n3"]
	if f.log.flapping["n2"] || f.log.neverMeta["n1"].label != "" || len(f.log.window) != 3 || len(f.log.burst) != 3 || !containsLabel(f.log.window, "q/n3") || !burstN3 {
		t.Errorf("removed server state survived refresh: flapping=%v never=%v window=%v burst=%v", f.log.flapping, f.log.neverMeta, f.log.window, f.log.burst)
	}
}

func containsLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

func TestNodeLogRefreshSerializesAnInFlightEvent(t *testing.T) {
	now := time.Unix(10_000, 0)
	entered, release := make(chan struct{}), make(chan struct{})
	block := func(string, ...any) { close(entered); <-release }
	l := newNodeLog([]stats.NodeInfo{{Label: "p/n0", Provider: "p", Country: "DE"}}, block, block, func() time.Time { return now })
	eventDone := make(chan struct{})
	go func() {
		l.event(core.NodeEvent{Label: "p/n0", Down: true, Reason: "timeout"})
		close(eventDone)
	}()
	<-entered
	refreshStarted, refreshDone := make(chan struct{}), make(chan struct{})
	go func() {
		close(refreshStarted)
		l.refresh([]stats.NodeInfo{{Label: "p/n0", Provider: "p2", Country: "US", Name: "updated"}}, nil)
		close(refreshDone)
	}()
	<-refreshStarted
	close(release)
	<-eventDone
	<-refreshDone
	if len(l.drops["n0"]) != 1 || l.byLabel["p/n0"].Country != "US" {
		t.Fatalf("in-flight event or refresh lost: drops=%v metadata=%+v", l.drops["n0"], l.byLabel["p/n0"])
	}
}

func TestNodeLogRefreshPreservesBurstPeak(t *testing.T) {
	f := newBurstFixture(t, 6)
	for i := 0; i < 4; i++ {
		f.down(i, "timeout")
	}
	f.back(0, time.Second)
	if f.log.burstSize != 4 || len(f.log.burst) != 3 {
		t.Fatalf("burst before refresh: peak=%d current=%d", f.log.burstSize, len(f.log.burst))
	}
	infos := make([]stats.NodeInfo, 0, len(f.log.byLabel))
	for _, n := range f.log.byLabel {
		infos = append(infos, n)
	}
	f.log.refresh(infos, nil)
	if f.log.burstSize != 4 || len(f.log.burst) != 3 {
		t.Fatalf("refresh lost burst peak: peak=%d current=%d", f.log.burstSize, len(f.log.burst))
	}
	for i := 1; i < 4; i++ {
		f.back(i, time.Second)
	}
	if got := f.lines[len(f.lines)-1]; got != "INFO node burst over: 4 nodes were down, all back after 0s" {
		t.Errorf("burst close after refresh = %q", got)
	}
}

func TestNodeLogRefreshRetainsFlappingAndNeverAnsweredState(t *testing.T) {
	f := newBurstFixture(t, 6).withDebug()
	for i := 0; i < 2; i++ {
		f.down(0, "timeout")
		f.back(0, time.Second)
		f.now = f.now.Add(time.Minute)
	}
	for i := 1; i <= 4; i++ {
		f.log.event(core.NodeEvent{Label: fmt.Sprintf("p/n%d", i), Down: true, NeverOK: true, Reason: "timeout"})
	}
	infos := make([]stats.NodeInfo, 0, len(f.log.byLabel))
	for _, n := range f.log.byLabel {
		n.Country = "US"
		infos = append(infos, n)
	}
	f.log.refresh(infos, nil)
	if len(f.log.drops["n0"]) != 2 || f.log.flapping["n0"] || f.log.neverSummary != 3 || len(f.log.neverLabels) != 4 {
		t.Fatalf("retained state changed: drops=%v flapping=%v summary=%d never=%v", f.log.drops["n0"], f.log.flapping["n0"], f.log.neverSummary, f.log.neverLabels)
	}
	f.down(0, "timeout")
	if got := f.countContaining("is flapping: 3 downs"); got != 1 {
		t.Fatalf("flapping history lost after refresh: %q", f.lines)
	}
	f.log.event(core.NodeEvent{Label: "p/n3", Down: true, NeverOK: true, Reason: "timeout"})
	if got := f.count("DEBUG node p/n3 (provider p, country US) has never answered: timeout"); got != 1 {
		t.Fatalf("never-answer repeat level/metadata after refresh: %q", f.lines)
	}
	f.log.event(core.NodeEvent{Label: "p/n5", Down: true, NeverOK: true, Reason: "timeout"})
	f.log.flushNow()
	if got := f.lines[len(f.lines)-1]; got != "INFO 5 servers have never answered since start (timeout x5; US x5); probed rarely, one DEBUG line each" {
		t.Errorf("never-answer summary after metadata refresh = %q", got)
	}
}
