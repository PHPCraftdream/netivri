package core

import (
	"context"
	"errors"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

// A dial with InUser set records exactly one account dial sample per logical
// dial, and the retry records one for its own label too.
func TestAccountObserverDial(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	obs := &recordedObserver{}
	g, _, health := newTestGroup(t, []*fakeMember{a, b}, obs)

	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	if _, err := g.DialContext(context.Background(), dialMetadata("alice", "www.example.com")); err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	samples := obs.accountDialSamples()
	if len(samples) != 1 {
		t.Fatalf("account dial samples = %+v, want exactly one", samples)
	}
	if s := samples[0]; s.login != "alice" || s.label != "b" || !s.ok || s.took < 0 {
		t.Fatalf("account dial sample = %+v, want {alice b true >=0}", s)
	}

	// Retry path: b fails its third consecutive dial, the retry on a is the
	// second logical dial and gets its own account sample.
	health.recordDial("b", false, 0)
	health.recordDial("b", false, 0)
	b.err = errors.New("refused")
	if _, err := g.DialContext(context.Background(), dialMetadata("alice", "www.example.com")); err != nil {
		t.Fatalf("retry dial failed: %v", err)
	}
	samples = obs.accountDialSamples()
	if len(samples) != 3 {
		t.Fatalf("account dial samples = %+v, want 3 (one per logical dial)", samples)
	}
	if s := samples[1]; s.login != "alice" || s.label != "b" || s.ok {
		t.Fatalf("failed account dial sample = %+v, want {alice b false}", s)
	}
	if s := samples[2]; s.login != "alice" || s.label != "a" || !s.ok {
		t.Fatalf("retry account dial sample = %+v, want {alice a true}", s)
	}
}

// plainObserver implements only Observer, not AccountObserver.
type plainObserver struct {
	dials []dialSample
}

func (o *plainObserver) ObserveProbe(label string, alive bool, delay time.Duration) {}
func (o *plainObserver) ObserveDial(label string, ok bool, took time.Duration) {
	o.dials = append(o.dials, dialSample{label: label, ok: ok, took: took})
}
func (o *plainObserver) ObserveThroughput(label string, bytes int64, window time.Duration) {}

var _ Observer = (*plainObserver)(nil)

// An observer without AccountObserver must not break the dial path.
func TestAccountObserverOptional(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	obs := &plainObserver{}
	g, _, health := newTestGroup(t, []*fakeMember{a, b}, obs)

	health.recordProbe("a", true, 100*time.Millisecond)
	health.recordProbe("b", true, 50*time.Millisecond)

	if _, err := g.DialContext(context.Background(), dialMetadata("alice", "www.example.com")); err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	if len(obs.dials) != 1 || !obs.dials[0].ok || obs.dials[0].label != "b" {
		t.Fatalf("dial samples = %+v, want one ok sample on b", obs.dials)
	}
}

// collectAt reports per (login, label) up/down deltas alongside the combined
// per-label sample, and never reports account traffic for a login-less
// tracker.
func TestThroughputPollerAccountThroughput(t *testing.T) {
	health := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, health, obs, map[string]string{"netivri-r0-0": "a"}, nil)

	base := time.Unix(0, 0)
	before := base.Add(-time.Minute)
	tracker := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{
		Metadata: &C.Metadata{InUser: "bob"},
		Chain:    C.Chain{"netivri-r0-0", "netivri-group-0"},
		Start:    before,
	}}
	loginLess := &fakeTracker{id: "t2", info: &statistic.TrackerInfo{
		Chain: C.Chain{"netivri-r0-0", "netivri-group-0"},
		Start: before,
	}}
	source := &fakeTrackerSource{trackers: []throughputTracker{tracker, loginLess}}
	poller.source = source

	// Baseline read.
	tracker.info.UploadTotal.Add(1 << 20)
	tracker.info.DownloadTotal.Add(1 << 20)
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)
	if len(obs.throughputSamples()) != 0 || len(obs.accountTrafficSamples()) != 0 {
		t.Fatalf("the baselining read reported movement")
	}

	// Second read: the deltas are reported per account.
	tracker.info.UploadTotal.Add(3 << 20)
	tracker.info.DownloadTotal.Add(1 << 20)
	poller.collectAt(base.Add(5*time.Second), base.Add(10*time.Second), 5*time.Second)

	account := obs.accountTrafficSamples()
	if len(account) != 1 {
		t.Fatalf("account throughput samples = %+v, want one", account)
	}
	if s := account[0]; s.login != "bob" || s.label != "a" || s.up != 3<<20 || s.down != 1<<20 || s.window != 5*time.Second {
		t.Fatalf("account throughput sample = %+v, want {bob a 3MiB 1MiB 5s}", s)
	}
	combined := obs.throughputSamples()
	if len(combined) != 1 {
		t.Fatalf("throughput samples = %+v, want one", combined)
	}
	if s := combined[0]; s.label != "a" || s.bytes != (3<<20)+(1<<20) {
		t.Fatalf("combined throughput sample = %+v, want {a, 4 MiB}", s)
	}

	// A login-less tracker with movement must not produce an account sample.
	loginLess.info.UploadTotal.Add(2 << 20)
	poller.collectAt(base.Add(10*time.Second), base.Add(15*time.Second), 5*time.Second)
	account = obs.accountTrafficSamples()
	if len(account) != 1 {
		t.Fatalf("account throughput samples after login-less tracker = %+v, want still one", account)
	}
}

// collectAt must merge all logins on a node into one per-node throughput
// sample: two accounts moving 10 KiB each through the same node in one
// window produce a single 20 KiB node sample (above the 16 KiB node gate),
// and no account samples, because the 16 KiB account gate applies to each
// per-(login, label) entry separately (10 KiB each, both dropped).
func TestCollectAtNodeTotalSpansAccounts(t *testing.T) {
	health := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, health, obs, map[string]string{"netivri-r0-0": "a"}, nil)

	base := time.Unix(0, 0)
	before := base.Add(-time.Minute)
	alice := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{
		Metadata: &C.Metadata{InUser: "alice"},
		Chain:    C.Chain{"netivri-r0-0", "netivri-group-0"},
		Start:    before,
	}}
	bob := &fakeTracker{id: "t2", info: &statistic.TrackerInfo{
		Metadata: &C.Metadata{InUser: "bob"},
		Chain:    C.Chain{"netivri-r0-0", "netivri-group-0"},
		Start:    before,
	}}
	poller.source = &fakeTrackerSource{trackers: []throughputTracker{alice, bob}}

	// Baseline read.
	alice.info.UploadTotal.Add(1 << 20)
	bob.info.UploadTotal.Add(1 << 20)
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)
	if len(obs.throughputSamples()) != 0 || len(obs.accountTrafficSamples()) != 0 {
		t.Fatalf("the baselining read reported movement")
	}

	// Each account moves 10 KiB through the same node between ticks.
	alice.info.UploadTotal.Add(10 << 10)
	bob.info.UploadTotal.Add(10 << 10)
	poller.collectAt(base.Add(5*time.Second), base.Add(10*time.Second), 5*time.Second)

	// The node total (20 KiB) passes the 16 KiB node gate and must be a
	// single merged sample, not two 10 KiB samples.
	node := obs.throughputSamples()
	if len(node) != 1 {
		t.Fatalf("node throughput samples = %+v, want exactly one merged sample", node)
	}
	if s := node[0]; s.label != "a" || s.bytes != 20<<10 || s.window != 5*time.Second {
		t.Fatalf("node throughput sample = %+v, want {a, 20KiB, 5s}", s)
	}
	// Each per-account entry is only 10 KiB, below the 16 KiB account gate,
	// so no account samples may be emitted.
	if account := obs.accountTrafficSamples(); len(account) != 0 {
		t.Fatalf("account traffic samples = %+v, want none (10 KiB entries below the 16 KiB per-entry account gate)", account)
	}
}

// Like the 10 KiB case but with 1 MiB per account: the node sample must be
// the merged 2 MiB total while each account still gets its own sample.
func TestCollectAtNodeTotalMergesLargeAccounts(t *testing.T) {
	health := newHealthTable()
	obs := &recordedObserver{}
	poller := newThroughputPoller(nil, health, obs, map[string]string{"netivri-r0-0": "a"}, nil)

	base := time.Unix(0, 0)
	before := base.Add(-time.Minute)
	alice := &fakeTracker{id: "t1", info: &statistic.TrackerInfo{
		Metadata: &C.Metadata{InUser: "alice"},
		Chain:    C.Chain{"netivri-r0-0", "netivri-group-0"},
		Start:    before,
	}}
	bob := &fakeTracker{id: "t2", info: &statistic.TrackerInfo{
		Metadata: &C.Metadata{InUser: "bob"},
		Chain:    C.Chain{"netivri-r0-0", "netivri-group-0"},
		Start:    before,
	}}
	poller.source = &fakeTrackerSource{trackers: []throughputTracker{alice, bob}}

	alice.info.UploadTotal.Add(1 << 20)
	bob.info.UploadTotal.Add(1 << 20)
	poller.collectAt(base, base.Add(5*time.Second), 5*time.Second)

	alice.info.UploadTotal.Add(1 << 20)
	bob.info.UploadTotal.Add(1 << 20)
	poller.collectAt(base.Add(5*time.Second), base.Add(10*time.Second), 5*time.Second)

	node := obs.throughputSamples()
	if len(node) != 1 {
		t.Fatalf("node throughput samples = %+v, want exactly one merged sample", node)
	}
	if s := node[0]; s.label != "a" || s.bytes != 2<<20 || s.window != 5*time.Second {
		t.Fatalf("node throughput sample = %+v, want {a, 2MiB, 5s}", s)
	}

	account := obs.accountTrafficSamples()
	if len(account) != 2 {
		t.Fatalf("account traffic samples = %+v, want two (alice and bob)", account)
	}
	got := map[string]accountTrafficSample{}
	for _, s := range account {
		got[s.login] = s
	}
	for login := range map[string]struct{}{"alice": {}, "bob": {}} {
		s, ok := got[login]
		if !ok {
			t.Fatalf("no account traffic sample for %s in %+v", login, account)
		}
		if s.label != "a" || s.up != 1<<20 || s.down != 0 || s.window != 5*time.Second {
			t.Fatalf("%s account traffic sample = %+v, want {a, up 1MiB, down 0, 5s}", login, s)
		}
	}
}
