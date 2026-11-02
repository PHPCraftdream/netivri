package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

const (
	// burstWindow and burstNodes: that many nodes going down within the window
	// are one event (a lost network, a shared outage), not that many faults.
	burstWindow = 10 * time.Second
	burstNodes  = 3
	// startupWindow is how long after the angel started a burst is reported as
	// a start-up one.
	startupWindow = time.Minute
	// flapWindow and flapDowns: a node that goes down that many times within
	// the window is flapping, which is one warning and not one per drop.
	flapWindow = time.Hour
	flapDowns  = 3
	// twinWindow: the same server listed by two providers has two labels and
	// is probed twice, so its down reports come together; a second one within
	// the window is the same event.
	twinWindow = 10 * time.Second
	// outageMinNodes and the half rule: a network that fails slowly (nodes are
	// probed minutes apart, so their downs trickle in) is no burst. When that
	// many servers that have answered before are down at once, and they are at
	// least half of those, it is an outage; it ends when fewer than a quarter
	// are still down.
	outageMinNodes = 6
)

type logFunc = func(format string, args ...any)

// nodeHealthLogger returns the core.Options.OnNodeHealth hook that writes a
// WARN line when a route node goes down and an INFO line when it is back, with
// the node label, provider and country (never an address or a URL), the reason
// it went down and how long it was down. Nodes going down together are
// summed up in one WARN line, see nodeLog. Without route nodes there is nothing
// to report and it returns nil. first holds the labels of the first-tier
// nodes of the routes: only their downs are warnings, the nodes of fallback
// tiers (no account uses them while the first tier is up) are INFO. A nil
// first makes every node a warning. quiet, when it says true (the computer
// woke up a moment ago), turns the downs into plain INFO lines that count for
// nothing else; nil is never quiet.
func nodeHealthLogger(infos []stats.NodeInfo, first map[string]bool, quiet func() bool, debug, info, warn logFunc) func(core.NodeEvent) {
	l := newNodeLog(infos, info, warn, time.Now)
	if l == nil {
		return nil
	}
	if debug != nil {
		l.debug = debug
	}
	l.first = first
	l.quiet = quiet
	return l.event
}

// nodeLog turns node down/back events into log lines. A node that has never
// answered (it did not go down, it never came up) is one INFO line and takes
// no part in the rest. A node that keeps going
// down (flapDowns times within flapWindow) is flapping: one WARN says so, its
// further down and back lines are INFO until it has been quiet for flapWindow,
// and it does not count toward a burst. One server under two labels (the same
// id from two providers) is one node for all of this: its twin's down within
// twinWindow is an INFO line and counts for nothing. For the others, the first burstNodes-1
// nodes that go down within burstWindow get a WARN line each. The next one
// also raises a single WARN summary ("node burst: ...") and from then on the
// per-node lines of the burst are INFO, so a network loss that takes ten nodes
// down at once reads as one warning. When the last node of a burst is back an
// INFO line closes it.
// A network that fails slowly makes no burst: when at least outageMinNodes
// servers that have answered before are down at once, and half of them or
// more, one WARN ("node outage: ...") says so, the down and back lines of the
// nodes are DEBUG while it lasts, and an INFO line ends it when fewer than a
// quarter are still down. A burst in progress keeps its own lines.
type nodeLog struct {
	info, warn logFunc
	debug      logFunc // repeats of fallback nodes; INFO unless set
	now        func() time.Time
	byLabel    map[string]stats.NodeInfo
	started    time.Time
	first      map[string]bool // first-tier labels; nil: every node warns
	quiet      func() bool     // true right after the computer woke up; may be nil

	mu          sync.Mutex
	drops       map[string][]drop // recent downs per server, within flapWindow
	flapping    map[string]bool   // by server
	lastDown    map[string]drop   // latest counted down per server, with the label
	lastNever   map[string]drop   // the same for never-answered reports
	lastBack    map[string]drop   // the same for backs
	windowStart time.Time
	window      []string       // labels down in the current window
	kinds       map[string]int // reasons in the current window
	burst       map[string]struct{}
	burstSize   int
	burstStart  time.Time
	// The outage state: servers (not labels) that are down now, servers that
	// never answered, and how many servers there are.
	servers     int
	downNow     map[string]struct{}
	never       map[string]struct{}
	outage      bool
	outageStart time.Time
	outagePeak  int
}

// drop is one recent down of a server; label is the one that reported it.
type drop struct {
	at    time.Time
	kind  string
	label string
}

// serverID is the part of a label after the provider: the same id under two
// providers is one server.
func serverID(label string) string {
	if _, id, ok := strings.Cut(label, "/"); ok && id != "" {
		return id
	}
	return label
}

// newNodeLog returns nil when there are no nodes to report on.
func newNodeLog(infos []stats.NodeInfo, info, warn logFunc, now func() time.Time) *nodeLog {
	if len(infos) == 0 {
		return nil
	}
	l := &nodeLog{info: info, warn: warn, debug: info, now: now, byLabel: make(map[string]stats.NodeInfo, len(infos)), started: now(),
		drops: map[string][]drop{}, flapping: map[string]bool{}, lastDown: map[string]drop{}, lastNever: map[string]drop{}, lastBack: map[string]drop{},
		downNow: map[string]struct{}{}, never: map[string]struct{}{}}
	ids := map[string]struct{}{}
	for _, n := range infos {
		l.byLabel[n.Label] = n
		ids[serverID(n.Label)] = struct{}{}
	}
	l.servers = len(ids)
	return l
}

// event is the OnNodeHealth hook.
func (l *nodeLog) event(ev core.NodeEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.Down {
		l.down(ev)
		return
	}
	l.back(ev)
}

func (l *nodeLog) down(ev core.NodeEvent) {
	now := l.now()
	id := serverID(ev.Label)
	if ev.NeverOK {
		l.never[id] = struct{}{}
		line := l.describe(ev.Label) + " has never answered: " + ev.Reason
		if twin, ok := l.twinOf(l.lastNever, ev, now); ok {
			line += ", same server as " + twin
		}
		l.info("%s", line)
		return
	}
	delete(l.never, id)
	line := l.describe(ev.Label) + " is down: " + ev.Reason
	if ev.SinceOK > 0 {
		line += ", last ok " + ev.SinceOK.Round(time.Second).String() + " ago"
	}
	if twin, ok := l.twinOf(l.lastDown, ev, now); ok {
		l.repeat(ev.Label, "%s, same server as %s", line, twin)
		return
	}
	if l.quiet != nil && l.quiet() {
		l.info("%s, right after the computer woke up", line)
		return
	}
	l.downNow[id] = struct{}{} // the downs of the quiet time are noise and count for nothing
	if l.flaps(ev, now) {
		l.repeat(ev.Label, "%s", line)
		return
	}
	if l.outage {
		l.outagePeak = max(l.outagePeak, len(l.downNow))
		l.debug("%s", line)
		return
	}
	if l.burst == nil && l.outageBegins() {
		l.outage, l.outageStart, l.outagePeak = true, now, len(l.downNow)
		l.warn("node outage: %d of %d servers that have answered are down, likely a lost network or the provider; their down and back reports are DEBUG until most are back",
			len(l.downNow), l.live())
		l.debug("%s", line)
		return
	}
	if len(l.window) == 0 || now.Sub(l.windowStart) > burstWindow {
		l.windowStart, l.window, l.kinds = now, nil, map[string]int{}
	}
	l.window = append(l.window, ev.Label)
	l.kinds[reasonKind(ev.Reason)]++
	switch {
	case len(l.window) < burstNodes:
		l.nodeWarn(ev.Label, "%s", line)
	case len(l.window) == burstNodes:
		l.warn("%s", l.summary(now))
		l.info("%s", line)
		if l.burst == nil {
			l.burst = make(map[string]struct{}, len(l.window))
			l.burstStart = l.windowStart
		}
		for _, label := range l.window {
			l.burst[label] = struct{}{}
		}
		l.burstSize = max(l.burstSize, len(l.burst))
	default:
		l.info("%s", line)
		if l.burst != nil {
			l.burst[ev.Label] = struct{}{}
			l.burstSize = max(l.burstSize, len(l.burst))
		}
	}
}

// twinOf reports whether the same server was reported under another label
// within twinWindow (in latest), and which one. Otherwise it remembers this
// report as the latest one of the server.
func (l *nodeLog) twinOf(latest map[string]drop, ev core.NodeEvent, now time.Time) (string, bool) {
	id := serverID(ev.Label)
	if last, ok := latest[id]; ok && last.label != ev.Label && now.Sub(last.at) <= twinWindow {
		return last.label, true
	}
	latest[id] = drop{at: now, label: ev.Label}
	return "", false
}

// flaps records a down of the server and reports whether it is flapping.
// The drop that makes it flap raises the one WARN about it.
func (l *nodeLog) flaps(ev core.NodeEvent, now time.Time) bool {
	id := serverID(ev.Label)
	recent := l.drops[id][:0]
	for _, d := range l.drops[id] {
		if now.Sub(d.at) <= flapWindow {
			recent = append(recent, d)
		}
	}
	recent = append(recent, drop{at: now, kind: reasonKind(ev.Reason), label: ev.Label})
	l.drops[id] = recent
	if len(recent) < flapDowns {
		delete(l.flapping, id)
		return false
	}
	if !l.flapping[id] {
		l.flapping[id] = true
		kinds := map[string]int{}
		for _, d := range recent {
			kinds[d.kind]++
		}
		l.nodeWarn(ev.Label, "%s is flapping: %d downs within the last hour (%s); its further down and back reports are INFO",
			l.describe(ev.Label), len(recent), joinKinds(kinds))
	}
	return true
}

// repeat logs a line that adds nothing to what is already in the log (a down
// of a flapping server, a twin's down): INFO for a first-tier node, DEBUG for
// the others, so the log of an account is not filled by nodes it never uses.
func (l *nodeLog) repeat(label, format string, args ...any) {
	if l.first == nil || (l.first[label] && !l.outage) {
		l.info(format, args...)
		return
	}
	l.debug(format, args...)
}

// nodeWarn logs a per-node line at WARN for a first-tier node and at INFO for
// the others (see nodeHealthLogger).
func (l *nodeLog) nodeWarn(label, format string, args ...any) {
	if l.first == nil || l.first[label] {
		l.warn(format, args...)
		return
	}
	l.info(format, args...)
}

func (l *nodeLog) back(ev core.NodeEvent) {
	line := l.describe(ev.Label) + " is back"
	if ev.Downtime > 0 {
		line += " after " + ev.Downtime.Round(time.Second).String() + " down"
	}
	id := serverID(ev.Label)
	delete(l.never, id)
	delete(l.downNow, id)
	if twin, ok := l.twinOf(l.lastBack, ev, l.now()); ok {
		l.repeat(ev.Label, "%s, same server as %s", line, twin)
	} else if l.flapping[id] {
		l.repeat(ev.Label, "%s", line)
	} else if l.outage {
		l.debug("%s", line)
	} else {
		l.info("%s", line)
	}
	l.outageEnds()
	if _, in := l.burst[ev.Label]; !in {
		return
	}
	delete(l.burst, ev.Label)
	if len(l.burst) == 0 {
		l.info("node burst over: %d nodes were down, all back after %s", l.burstSize, l.now().Sub(l.burstStart).Round(time.Second))
		l.burst, l.burstSize = nil, 0
	}
}

// live is the number of servers that have answered at least once.
func (l *nodeLog) live() int { return max(l.servers-len(l.never), 1) }

// outageBegins reports whether the servers down now make an outage.
func (l *nodeLog) outageBegins() bool {
	return len(l.downNow) >= outageMinNodes && 2*len(l.downNow) >= l.live()
}

// outageEnds closes an outage once fewer than a quarter of the servers are
// down, with one INFO line.
func (l *nodeLog) outageEnds() {
	if !l.outage || 4*len(l.downNow) >= l.live() {
		return
	}
	l.info("node outage over: up to %d of %d servers were down, %d still down, after %s",
		l.outagePeak, l.live(), len(l.downNow), l.now().Sub(l.outageStart).Round(time.Second))
	l.outage, l.outagePeak = false, 0
}

// summary is the one WARN line of a burst.
func (l *nodeLog) summary(now time.Time) string {
	s := fmt.Sprintf("node burst: %d of %d nodes went down within %s (%s), likely a lost network or a shared outage",
		len(l.window), len(l.byLabel), burstWindow, joinKinds(l.kinds))
	if now.Sub(l.started) < startupWindow {
		s += ", right after start"
	}
	return s
}

// joinKinds lists down reasons with their counts, sorted: "a x2, b x1".
func joinKinds(kinds map[string]int) string {
	out := make([]string, 0, len(kinds))
	for k, n := range kinds {
		out = append(out, fmt.Sprintf("%s x%d", k, n))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// describe names a node: label, name when known, provider and country.
func (l *nodeLog) describe(label string) string {
	n := l.byLabel[label]
	country := n.Country
	if country == "" {
		country = unknownCountry
	}
	if n.Name != "" {
		return fmt.Sprintf("node %s (%s, provider %s, country %s)", label, n.Name, n.Provider, country)
	}
	return fmt.Sprintf("node %s (provider %s, country %s)", label, n.Provider, country)
}

// reasonKind drops the leading count of a down reason, so "2 probe failures
// (timeout)" and "3 probe failures (timeout)" are the same kind.
func reasonKind(reason string) string {
	if _, rest, ok := strings.Cut(reason, " "); ok {
		return rest
	}
	return reason
}
