package main

import (
	"strings"
	"testing"
	"time"
)

// lineContaining returns the first collected line that holds text, or "".
func (f *burstFixture) lineContaining(text string) string {
	for _, l := range f.lines {
		if strings.Contains(l, text) {
			return l
		}
	}
	return ""
}

func TestNodeLogFlappingStaysUntilAWholeQuietWindow(t *testing.T) {
	f := newBurstFixture(t, 3)
	downAfter := func(gap time.Duration) {
		f.now = f.now.Add(gap)
		f.down(0, "2 probe failures (timeout)")
	}
	downAfter(0)
	downAfter(time.Minute)
	downAfter(time.Minute) // the third down within the hour: flapping
	if got := f.countContaining("is flapping"); got != 1 {
		t.Fatalf("%d flapping lines after three downs, want 1: %q", got, f.lines)
	}
	// The first downs age out of the window, so each new down finds fewer than
	// flapDowns downs in it, but the node has not been quiet for a whole window:
	// it is the same trouble and is not announced again.
	downAfter(40 * time.Minute)
	downAfter(40 * time.Minute)
	downAfter(40 * time.Minute)
	if got := f.countContaining("is flapping"); got != 1 {
		t.Errorf("%d flapping lines while the node keeps going down, want 1: %q", got, f.lines)
	}
	if got := f.count("WARN node p/n0"); got != 3 { // two downs before it flapped and the notice
		t.Errorf("%d warnings of the node, want 3: %q", got, f.lines)
	}
	// A whole window without a down ends it: the next down is a plain WARN.
	downAfter(flapWindow + time.Minute)
	if got := f.count("WARN node p/n0"); got != 4 {
		t.Errorf("%d warnings after the quiet window, want 4: %q", got, f.lines)
	}
	// And it takes flapDowns downs again to flap again.
	downAfter(time.Minute)
	if got := f.countContaining("is flapping"); got != 1 {
		t.Errorf("flapping again after two downs: %q", f.lines)
	}
	downAfter(time.Minute)
	if got := f.countContaining("is flapping"); got != 2 {
		t.Errorf("%d flapping lines after the third down of the new round, want 2: %q", got, f.lines)
	}
}

func TestNodeLogFlappingExactlyAWindowApartStaysFlapping(t *testing.T) {
	f := newBurstFixture(t, 3)
	for range 3 {
		f.down(0, "2 probe failures (timeout)")
		f.now = f.now.Add(time.Minute)
	}
	f.now = f.now.Add(flapWindow - time.Minute) // exactly flapWindow after the last down
	f.down(0, "2 probe failures (timeout)")
	if got := f.countContaining("is flapping"); got != 1 {
		t.Fatalf("%d flapping lines, want 1: %q", got, f.lines)
	}
	if got := f.count("WARN node p/n0"); got != 3 { // not a fourth, plain warning
		t.Errorf("%d warnings, want 3: %q", got, f.lines)
	}
}

func TestNodeLogFlappingNoticeNamesTheLevelOfItsRepeats(t *testing.T) {
	f := debugFixture(t)
	for _, label := range []string{"own/a", "own/c"} {
		for range 3 {
			f.now = f.now.Add(time.Minute)
			f.downLabel(label)
		}
	}
	first := f.lineContaining("node own/a (provider own, country FI) is flapping")
	if !strings.HasSuffix(first, "its further down and back reports are INFO") {
		t.Errorf("first-tier notice = %q", first)
	}
	fallback := f.lineContaining("node own/c (provider own, country FI) is flapping")
	if !strings.HasSuffix(fallback, "its further down and back reports are DEBUG") {
		t.Errorf("fallback notice = %q", fallback)
	}
	// What the notice says is what the next lines are.
	f.now = f.now.Add(time.Minute)
	f.downLabel("own/a")
	f.downLabel("own/c")
	if f.count("INFO node own/a") != 2 || f.count("DEBUG node own/a") != 0 { // the third down and the one after the notice
		t.Errorf("first-tier repeats: %q", f.lines)
	}
	if f.count("DEBUG node own/c") != 2 { // the third down, and the one after the notice
		t.Errorf("fallback repeats: %q", f.lines)
	}
}

func TestNodeLogFlappingNoticeOfAnOutageSaysDebug(t *testing.T) {
	f := newBurstFixture(t, 12).withDebug()
	f.log.first = map[string]bool{"p/n0": true}
	f.slowly(1, 8) // an outage: the repeats of every node are DEBUG
	if f.count("WARN node outage:") != 1 {
		t.Fatalf("no outage: %q", f.lines)
	}
	for range 3 {
		f.down(0, "2 probe failures (timeout)")
		f.now = f.now.Add(time.Minute)
	}
	notice := f.lineContaining("node p/n0 (provider p, country DE) is flapping")
	if !strings.HasSuffix(notice, "its further down and back reports are DEBUG") {
		t.Errorf("notice during an outage = %q", notice)
	}
}

func TestNodeLogBurstOfFallbackNodesIsInfo(t *testing.T) {
	f := newBurstFixture(t, 12)
	f.log.first = map[string]bool{"p/n0": true}
	for i := 1; i <= 4; i++ {
		f.down(i, "2 probe failures (timeout)")
		f.now = f.now.Add(time.Second)
	}
	if got := f.count("WARN"); got != 0 {
		t.Errorf("%d warnings for nodes no account uses: %q", got, f.lines)
	}
	want := "INFO node burst: 3 of 12 nodes went down within 10s (probe failures (timeout) x3), likely a lost network or a shared outage"
	if got := f.count(want); got != 1 {
		t.Errorf("%d burst summaries at INFO, want 1: %q", got, f.lines)
	}
}

func TestNodeLogBurstWithAFirstTierNodeIsWarn(t *testing.T) {
	f := newBurstFixture(t, 12)
	f.log.first = map[string]bool{"p/n2": true}
	for i := range 3 {
		f.down(i, "2 probe failures (timeout)")
	}
	if got := f.count("WARN node burst: 3 of 12"); got != 1 {
		t.Errorf("%d burst warnings, want 1: %q", got, f.lines)
	}
	if got := f.count("INFO node burst:"); got != 0 {
		t.Errorf("the burst was also written at INFO: %q", f.lines)
	}
}

func TestNodeLogFirstTierNodeJoiningAFallbackBurstRaisesTheWarning(t *testing.T) {
	f := newBurstFixture(t, 12)
	f.log.first = map[string]bool{"p/n3": true, "p/n6": true}
	for i := range 3 {
		f.down(i, "2 probe failures (timeout)")
	}
	f.down(4, "2 probe failures (timeout)") // another fallback node: still INFO
	if got := f.count("WARN"); got != 0 {
		t.Fatalf("a burst of fallback nodes warned: %q", f.lines)
	}
	f.down(3, "2 probe failures (timeout)") // a first-tier node joins
	if got := f.count("WARN node burst: 5 of 12"); got != 1 {
		t.Fatalf("%d burst warnings after a first-tier node joined, want 1: %q", got, f.lines)
	}
	f.down(6, "2 probe failures (timeout)") // a second one does not warn again
	if got := f.count("WARN node burst"); got != 1 {
		t.Errorf("%d burst warnings, want 1: %q", got, f.lines)
	}
}

func TestNodeLogBurstWarningStateEndsWithTheBurst(t *testing.T) {
	f := newBurstFixture(t, 12)
	f.log.first = map[string]bool{"p/n3": true}
	for i := range 4 { // a burst that a first-tier node joins
		f.down(i, "2 probe failures (timeout)")
	}
	if got := f.count("WARN node burst"); got != 1 {
		t.Fatalf("%d burst warnings, want 1: %q", got, f.lines)
	}
	f.now = f.now.Add(time.Minute)
	for i := range 4 {
		f.back(i, time.Minute)
	}
	if f.count("INFO node burst over") != 1 {
		t.Fatalf("the burst did not close: %q", f.lines)
	}
	// The next burst is judged on its own: fallback nodes only, INFO.
	infoBefore := f.count("INFO node burst: 3 of 12") // the first burst began as a fallback one
	f.now = f.now.Add(time.Hour)
	for i := 5; i < 8; i++ {
		f.down(i, "2 probe failures (timeout)")
	}
	if got := f.count("WARN node burst"); got != 1 {
		t.Errorf("a new burst of fallback nodes warned: %q", f.lines)
	}
	if got := f.count("INFO node burst: 3 of 12"); got != infoBefore+1 {
		t.Errorf("%d INFO summaries after the new burst, want %d: %q", got, infoBefore+1, f.lines)
	}
}

func TestNodeLogWithoutTierInformationEveryBurstWarns(t *testing.T) {
	f := newBurstFixture(t, 6)
	for i := range 3 {
		f.down(i, "2 probe failures (timeout)")
	}
	if got := f.count("WARN node burst: 3 of 6"); got != 1 {
		t.Errorf("%d burst warnings with a nil first-tier set, want 1: %q", got, f.lines)
	}
}
