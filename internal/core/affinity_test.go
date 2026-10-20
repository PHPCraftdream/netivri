package core

import (
	"strconv"
	"testing"
	"time"
)

func fakeClock(start time.Time) (*fakeClockNow, func() time.Time) {
	c := &fakeClockNow{now: start}
	return c, c.Now
}

type fakeClockNow struct {
	now time.Time
}

func (c *fakeClockNow) Now() time.Time { return c.now }

func (c *fakeClockNow) advance(d time.Duration) { c.now = c.now.Add(d) }

func noDownMember(int) bool { return false }

func TestAffinityTableAcquireRefreshesLastUsed(t *testing.T) {
	clock, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, time.Hour)
	key := affinityKey{login: "alice", site: "example.com"}
	picks := 0
	pick := func() (int, bool) { picks++; return 2, false }

	if member, rebound := table.acquire(key, noDownMember, pick); member != 2 || rebound {
		t.Fatalf("acquire = %d, %v, want 2, false", member, rebound)
	}
	clock.advance(30 * time.Minute)
	if member, rebound := table.acquire(key, noDownMember, pick); member != 2 || rebound {
		t.Fatalf("acquire = %d, %v, want 2, false", member, rebound)
	}
	if picks != 1 {
		t.Fatalf("pick ran %d times, want 1 (the live binding is reused)", picks)
	}
	// 75 minutes after the binding was made, 45 after its last use: the
	// refresh at +30m keeps it alive past the 1 hour TTL of its creation.
	clock.advance(45 * time.Minute)
	if _, ok := table.peek(key); !ok {
		t.Fatal("acquire did not refresh lastUsed: the binding expired early")
	}
	clock.advance(time.Hour)
	if _, ok := table.peek(key); ok {
		t.Fatal("binding survived its idle TTL")
	}
	if table.count() != 0 {
		t.Fatalf("count = %d, want 0 after expiry", table.count())
	}
}

func TestAffinityTableDefaultTTL(t *testing.T) {
	clock, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, 0)
	key := affinityKey{login: "alice", site: "example.com"}
	table.acquire(key, noDownMember, func() (int, bool) { return 0, false })
	clock.advance(defaultAffinityTTL - time.Minute)
	if _, ok := table.peek(key); !ok {
		t.Fatal("binding expired before the default TTL")
	}
	clock.advance(2 * time.Minute)
	if _, ok := table.peek(key); ok {
		t.Fatal("binding survived the default TTL")
	}
}

func TestAffinityTableProvisionalExpiry(t *testing.T) {
	clock, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, 0)
	key := affinityKey{login: "alice", site: "example.com"}
	if _, rebound := table.acquire(key, noDownMember, func() (int, bool) { return 1, true }); rebound {
		t.Fatal("a fresh binding is not a rebind")
	}
	clock.advance(time.Minute)
	if _, ok := table.peek(key); !ok {
		t.Fatal("provisional binding expired early")
	}
	clock.advance(2 * time.Minute)
	if _, ok := table.peek(key); ok {
		t.Fatal("provisional binding survived its 2 minute lifetime")
	}
}

func TestAffinityTableRebindWhenDown(t *testing.T) {
	_, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, 0)
	key := affinityKey{login: "alice", site: "example.com"}
	// First pick installs member 1, later picks install member 2.
	picked := 1
	pick := func() (int, bool) { p := picked; picked = 2; return p, false }

	if member, rebound := table.acquire(key, noDownMember, pick); member != 1 || rebound {
		t.Fatalf("acquire = %d, %v, want 1, false", member, rebound)
	}
	// Member 1 goes down: the next acquire rebinds exactly once.
	member1Down := func(member int) bool { return member == 1 }
	if member, rebound := table.acquire(key, member1Down, pick); member != 2 || !rebound {
		t.Fatalf("acquire = %d, %v, want 2, true", member, rebound)
	}
	if table.rebindCount() != 1 {
		t.Fatalf("rebinds = %d, want 1", table.rebindCount())
	}
	// The binding now points at the healthy member 2: no further rebinds.
	if member, rebound := table.acquire(key, member1Down, pick); member != 2 || rebound {
		t.Fatalf("acquire = %d, %v, want 2, false", member, rebound)
	}
	if table.rebindCount() != 1 {
		t.Fatalf("rebinds = %d, want still 1", table.rebindCount())
	}
}

func TestAffinityTableCountExcludesExpired(t *testing.T) {
	clock, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, time.Hour)
	expired := affinityKey{login: "alice", site: "expired.example"}
	live := affinityKey{login: "alice", site: "live.example"}
	table.acquire(expired, noDownMember, func() (int, bool) { return 0, false })
	clock.advance(2 * time.Hour)
	table.acquire(live, noDownMember, func() (int, bool) { return 0, false })
	if got := table.count(); got != 1 {
		t.Fatalf("count = %d, want 1 (expired bindings are excluded, not deleted)", got)
	}
	if _, ok := table.peek(expired); ok {
		t.Fatal("peek must respect expiry")
	}
	if _, ok := table.peek(live); !ok {
		t.Fatal("peek lost the live binding")
	}
}

func TestAffinityTableEvictionAtShardCap(t *testing.T) {
	clock, now := fakeClock(time.Unix(0, 0))
	table := newAffinityTable(now, 0)
	// Pick keys that all land in shard 0 and lower only that shard's cap so
	// the test stays small.
	login := "user"
	var keys []affinityKey
	for i := 0; len(keys) < 6; i++ {
		key := affinityKey{login: login, site: "site-" + strconv.Itoa(i)}
		if table.shardIndex(login, key.site) == 0 {
			keys = append(keys, key)
		}
	}
	shard := table.shards[0]
	shard.mu.Lock()
	shard.cap = 5
	shard.mu.Unlock()

	pick := func() (int, bool) { return 0, false }
	for _, key := range keys {
		clock.advance(time.Millisecond)
		table.acquire(key, noDownMember, pick)
	}
	if got := table.count(); got != 5 {
		t.Fatalf("count = %d, want the shard cap 5", got)
	}
	if _, ok := table.peek(keys[0]); ok {
		t.Fatal("the oldest binding was not evicted")
	}
	if _, ok := table.peek(keys[1]); !ok {
		t.Fatal("a recent binding was evicted")
	}
}

// affinityHash must equal FNV-1a over "login|site" so shard placement
// matches the documented key.
func TestAffinityHashMatchesConcatenatedFNV(t *testing.T) {
	if affinityHash("alice", "example.com") != fnv64a("alice|example.com") {
		t.Fatal("affinityHash is not FNV-1a of login|site")
	}
}

func TestSelectMemberBestWins(t *testing.T) {
	views := []memberHealthView{
		{Label: "a", Known: true, DelayMS: 100, Score: 100},
		{Label: "b", Known: true, DelayMS: 50, Score: 50},
	}
	if got := selectMember(views, "alice", "example.com"); got != 1 {
		t.Fatalf("selectMember = %d, want 1 (the best node)", got)
	}
}

// fnv64aRef is an independent FNV-1a implementation used only to compute
// test expectations; it shares no code with the production helper.
func fnv64aRef(s string) uint64 {
	const offset64 = 14695981039346656037
	const prime64 = 1099511628211
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// The two scores are near-equal (105 is within max(20 ms, 15 % of 100) of
// 100): the winner is the member whose fnv64a(login|site|label) is higher,
// computed here with an independent implementation.
func TestSelectMemberNearEqualTieByHash(t *testing.T) {
	views := []memberHealthView{
		{Label: "n1", Known: true, DelayMS: 100, Score: 100},
		{Label: "n2", Known: true, DelayMS: 105, Score: 105},
	}
	const login, site = "alice", "example.com"
	want := 0
	if fnv64aRef(login+"|"+site+"|n2") > fnv64aRef(login+"|"+site+"|n1") {
		want = 1
	}
	if got := selectMember(views, login, site); got != want {
		t.Fatalf("selectMember = %d, want %d (higher FNV-1a of login|site|label)", got, want)
	}
	if again := selectMember(views, login, site); again != want {
		t.Fatalf("selection is not stable: %d then %d", want, again)
	}
}

func TestSelectMemberNearEqualExcludesFarNodes(t *testing.T) {
	views := []memberHealthView{
		{Label: "far", Known: true, DelayMS: 200, Score: 200},
		{Label: "best", Known: true, DelayMS: 100, Score: 100},
	}
	// The far node (200 > 100+20) must never win the tie.
	if got := selectMember(views, "alice", "example.com"); got != 1 {
		t.Fatalf("selectMember = %d, want 1", got)
	}
}

func TestSelectMemberExcludesDown(t *testing.T) {
	views := []memberHealthView{
		{Label: "a", Known: true, DelayMS: 10, Score: 10, Down: true},
		{Label: "b", Known: true, DelayMS: 50, Score: 50},
	}
	if got := selectMember(views, "alice", "example.com"); got != 1 {
		t.Fatalf("selectMember = %d, want 1 (a is down)", got)
	}
}

func TestSelectMemberAllDownUsesAllMembers(t *testing.T) {
	views := []memberHealthView{
		{Label: "a", Known: true, Score: 10, Down: true},
		{Label: "b", Known: true, Score: 50, Down: true},
	}
	got := selectMember(views, "alice", "example.com")
	if got != 0 && got != 1 {
		t.Fatalf("selectMember = %d, want a member of the route", got)
	}
}

func TestSelectMemberUnknownDelaysUsesHash(t *testing.T) {
	views := []memberHealthView{{Label: "a"}, {Label: "b"}}
	got := selectMember(views, "alice", "example.com")
	if got != 0 && got != 1 {
		t.Fatalf("selectMember = %d, want a member index", got)
	}
	if again := selectMember(views, "alice", "example.com"); again != got {
		t.Fatalf("selection without delay data is not stable: %d then %d", got, again)
	}
}

// The provisional rule is "more than half of the members have no delay
// data": exactly half is NOT provisional.
func TestProvisionalBindingRule(t *testing.T) {
	allKnown := []memberHealthView{{Label: "a", Known: true}, {Label: "b", Known: true}}
	if provisionalBinding(allKnown) {
		t.Fatal("all known must not be provisional")
	}
	exactHalf := []memberHealthView{{Label: "a", Known: true}, {Label: "b"}}
	if provisionalBinding(exactHalf) {
		t.Fatal("exactly half unknown must not be provisional")
	}
	majorityUnknown := []memberHealthView{{Label: "a"}, {Label: "b"}, {Label: "c", Known: true}}
	if !provisionalBinding(majorityUnknown) {
		t.Fatal("more than half unknown must be provisional")
	}
	single := []memberHealthView{{Label: "a"}}
	if !provisionalBinding(single) {
		t.Fatal("the only member unknown must be provisional")
	}
}

func TestBuildRoutePlansLabels(t *testing.T) {
	o := Options{
		RouteNodes: map[string][]map[string]any{
			"de": {testRouteNode("a", "a.example", 1080), testRouteNode("b", "b.example", 1081)},
			"nl": {testRouteNode("c", "c.example", 1082)},
		},
		AccountRoute: map[string]string{"alice": "de", "bob": "nl"},
	}
	plans, err := buildRoutePlans(o)
	if err != nil {
		t.Fatal(err)
	}
	// Keys are indexed in sorted order: de = 0, nl = 1.
	if len(plans) != 2 || plans[0].index != 0 || plans[1].index != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	if plans[0].names[0] != "netivri-r0-0" || plans[0].names[1] != "netivri-r0-1" {
		t.Fatalf("names = %v", plans[0].names)
	}
	if plans[0].labels[0] != "r0-0" || plans[0].labels[1] != "r0-1" {
		t.Fatalf("default labels = %v", plans[0].labels)
	}
}

func TestBuildRoutePlansInvalidLabels(t *testing.T) {
	newOptions := func(labels map[string][]string) Options {
		return Options{
			RouteNodes: map[string][]map[string]any{
				"de": {testRouteNode("a", "a.example", 1080)},
				"nl": {testRouteNode("b", "b.example", 1081)},
			},
			AccountRoute: map[string]string{"alice": "de"},
			NodeLabels:   labels,
		}
	}
	for _, test := range []struct {
		name   string
		labels map[string][]string
	}{
		{name: "wrong length on a used route", labels: map[string][]string{"de": {"a", "b"}}},
		{name: "empty label", labels: map[string][]string{"de": {""}}},
		{name: "too long", labels: map[string][]string{"de": {string(make([]byte, 129))}}},
		{name: "control character", labels: map[string][]string{"de": {"a\x01b"}}},
		{
			name:   "wrong length on an unused route key",
			labels: map[string][]string{"nl": {"a", "b"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildRoutePlans(newOptions(test.labels)); err == nil {
				t.Fatal("invalid labels accepted")
			}
		})
	}
	t.Run("valid labels on an unused route key", func(t *testing.T) {
		if _, err := buildRoutePlans(newOptions(map[string][]string{"nl": {"nl-node-1"}})); err != nil {
			t.Fatalf("valid labels rejected: %v", err)
		}
	})
	t.Run("malformed labels are rejected without any account route", func(t *testing.T) {
		o := newOptions(map[string][]string{"de": {"a", "b"}})
		o.AccountRoute = nil
		if _, err := buildRoutePlans(o); err == nil {
			t.Fatal("malformed labels accepted when no account has a route")
		}
	})
	t.Run("labels of unknown route keys are ignored", func(t *testing.T) {
		if _, err := buildRoutePlans(newOptions(map[string][]string{"no-such-route": {""}})); err != nil {
			t.Fatalf("labels for an unknown route key were not ignored: %v", err)
		}
	})

	t.Run("message hides label", func(t *testing.T) {
		_, err := buildRoutePlans(newOptions(map[string][]string{"de": {"wrong", "length"}}))
		if err == nil {
			t.Fatal("wrong length accepted")
		}
		if containsString(err.Error(), "wrong") {
			t.Fatalf("error leaked the label: %v", err)
		}
	})
	t.Run("message hides key", func(t *testing.T) {
		o := Options{
			RouteNodes: map[string][]map[string]any{
				"secret-key": {testRouteNode("a", "a.example", 1080)},
			},
			AccountRoute: map[string]string{"alice": "secret-key"},
			NodeLabels:   map[string][]string{"secret-key": {"a", "b"}},
		}
		_, err := buildRoutePlans(o)
		if err == nil {
			t.Fatal("wrong length accepted")
		}
		if containsString(err.Error(), "secret-key") {
			t.Fatalf("error leaked the route key: %v", err)
		}
	})
}

func containsString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
