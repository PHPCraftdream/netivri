package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
	C "github.com/metacubex/mihomo/constant"
)

func TestPoolPrecedenceAndOpaquePoolKeys(t *testing.T) {
	o := Options{
		RoutePre:     map[string]string{"r": "socks5://legacy.invalid:1080"},
		RoutePrePool: map[string][]string{"r": {"socks5://route.invalid:1080", "none"}},
		NodePrePool:  map[string][][]string{"r": {nil}},
	}
	if got := o.nodePrePool("r", 0); len(got) != 0 {
		t.Fatal("node-level empty pool did not override route pool")
	}
	delete(o.NodePrePool, "r")
	if got := o.nodePrePool("r", 0); len(got) != 1 || got[0] != "socks5://route.invalid:1080" {
		t.Fatal("route pool did not override scalar")
	}
	a := hopPoolKey("socks5://first:fake@socks.invalid:1080")
	b := hopPoolKey("socks5://second:fake@socks.invalid:1080")
	if a == "" || a != b || len(a) != 64 {
		t.Fatal("credentials affected opaque hop identity")
	}
	zoneA := hopPoolKey("socks5://u:p@[2001:db8::1%25zone]:1080")
	zoneB := hopPoolKey("socks5://changed:credentials@[2001:db8::1%25zone]:1080")
	if !strings.HasPrefix(zoneA, "u:") || zoneA != zoneB {
		t.Fatal("valid zoned IPv6 identity did not use credential-independent fallback")
	}
}

func TestPoolFailureClassificationUsesStageEvidence(t *testing.T) {
	cases := []struct {
		name, err, next, addr, proto, carrier string
		stage                                 pool.Stage
		kind                                  pool.Kind
		known                                 bool
	}{
		{"current connect", "hop.invalid:1080 connect error: connection refused", "", "hop.invalid:1080", "socks5", "vless", pool.Connect, pool.Unreachable, true},
		{"HTTP auth", "HTTP need auth", "node.invalid:443", "hop.invalid:80", "http", "vless", pool.Refusal, pool.AuthPolicy, true},
		{"HTTP target", "502 Bad Gateway", "node.invalid:443", "hop.invalid:80", "http", "vless", pool.Target, pool.NextUnreachable, true},
		{"HTTP target error code", "err code: 503", "node.invalid:443", "hop.invalid:80", "http", "vless", pool.Target, pool.NextUnreachable, true},
		{"HTTP port digits", "dial host:5023: connection refused", "", "hop.invalid:80", "http", "vless", pool.UnknownStage, pool.UnknownKind, false},
		{"SOCKS target", "SOCKS error: 4", "", "hop.invalid:1080", "socks5", "vless", pool.Target, pool.NextUnreachable, true},
		{"unknown EOF", "unexpected EOF", "", "hop.invalid:1080", "socks5", "vless", pool.UnknownStage, pool.UnknownKind, false},
		{"capacity", "max connections reached", "", "hop.invalid:1080", "socks5", "vless", pool.UnknownStage, pool.Capacity, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stage, kind, known := classifyPoolFailureAt(errors.New(tc.err), tc.next, tc.addr, tc.proto, tc.carrier)
			if stage != tc.stage || kind != tc.kind || known != tc.known {
				t.Fatalf("classification=(%v,%v,%v), want (%v,%v,%v)", stage, kind, known, tc.stage, tc.kind, tc.known)
			}
		})
	}
	if _, _, known := classifyPoolFailureAt(fmt.Errorf("wrapped: %w", context.Canceled), "", "hop.invalid:1080", "socks5", "vless"); known {
		t.Fatal("canceled dial was attributed")
	}
}

func TestPrePoolUsesLibraryFailoverAndTypedHopFailure(t *testing.T) {
	spec := newHopPoolSpec(preHopPool, []string{"socks5://a.invalid:1080", "socks5://b.invalid:1080"}, 0)
	now := time.Unix(100, 0)
	seed := [2]uint64{3, 4}
	order := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])}).Order(now)
	if len(order) != 2 {
		t.Fatalf("pool order=%v, want two hops", order)
	}
	failedAddress := ""
	for i, key := range spec.keys {
		if key == order[0] {
			failedAddress = hopAddress(spec.uris[i])
			break
		}
	}
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	members := map[string]member{
		order[0]: &fakeMember{name: "failed", err: fmt.Errorf("%s connect error: connection refused", failedAddress)},
		order[1]: &fakeMember{name: "success"},
	}
	a := newHopPoolAdapter("pre", preHopPool, spec, members, p, nil, nil, func() time.Time { return now })
	var mutations int
	a.mutate = func(_ string, change func()) { mutations++; change() }
	conn, err := a.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "example.test", DstPort: 443})
	if err != nil || conn == nil {
		t.Fatalf("library failover failed: %v", err)
	}
	if len(conn.Chains()) != 1 || conn.Chains()[0] != "pre" {
		t.Fatalf("connection chain = %v", conn.Chains())
	}
	if mutations != 2 {
		t.Fatalf("health mutations=%d, want one failure and one success", mutations)
	}
	for _, key := range spec.keys {
		if got := members[key].(*fakeMember).dials.Load(); got != 1 {
			t.Fatalf("hop %s attempts=%d, want one", key, got)
		}
	}

	unknown := newHopPoolAdapter("unknown", preHopPool, spec, map[string]member{
		spec.keys[0]: &fakeMember{name: "a", err: errors.New("unexpected EOF")},
		spec.keys[1]: &fakeMember{name: "b", err: errors.New("unexpected EOF")},
	}, pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(1, 2)}), nil, nil, func() time.Time { return now })
	unknown.mutate = func(_ string, change func()) { t.Fatal("unknown failure mutated health") }
	_, err = unknown.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, Host: "example.test", DstPort: 443})
	if err == nil || !isHopPoolDialError(err) || errors.Is(err, errors.New("unexpected EOF")) {
		t.Fatalf("unknown failure was not safely wrapped: %v", err)
	}
}

type testSiteBinder struct{ values map[string]string }

func (b *testSiteBinder) Get(login, site string) (string, bool) {
	value, ok := b.values[login+"\x00"+site]
	return value, ok
}
func (b *testSiteBinder) Bind(login, site, key string) { b.values[login+"\x00"+site] = key }
func (b *testSiteBinder) Unbind(login, site string)    { delete(b.values, login+"\x00"+site) }
func (b *testSiteBinder) Forget(key string) int {
	n := 0
	for entry, value := range b.values {
		if value == key {
			delete(b.values, entry)
			n++
		}
	}
	return n
}

func TestPostPoolBindsAndReusesTheLibrarySiteChoice(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{"http://a.invalid:8080", "http://b.invalid:8080"}, 0)
	now := time.Unix(200, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(3, 4)})
	binder := &testSiteBinder{values: make(map[string]string)}
	members := map[string]member{spec.keys[0]: &fakeMember{name: "a"}, spec.keys[1]: &fakeMember{name: "b"}}
	carrier := &nodeHealth{now: func() time.Time { return now }}
	carrier.recordProbe(true, time.Millisecond)
	first := newHopPoolAdapter("post-1", postHopPool, spec, members, p, binder, carrier, func() time.Time { return now })
	metadata := &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443}
	if _, err := first.DialContext(context.Background(), metadata); err != nil {
		t.Fatalf("post pool dial: %v", err)
	}
	bound, ok := binder.Get("alice", "example.test")
	if !ok {
		t.Fatal("successful post dial did not bind site")
	}
	before0, before1 := members[spec.keys[0]].(*fakeMember).dials.Load(), members[spec.keys[1]].(*fakeMember).dials.Load()
	second := newHopPoolAdapter("post-2", postHopPool, spec, members, p, binder, carrier, func() time.Time { return now })
	if _, err := second.DialContext(context.Background(), metadata); err != nil {
		t.Fatalf("bound post dial: %v", err)
	}
	if members[bound].(*fakeMember).dials.Load() != map[string]int32{spec.keys[0]: before0 + 1, spec.keys[1]: before1 + 1}[bound] {
		t.Fatal("bound hop was not reused across node adapters")
	}
	other := spec.keys[0]
	if other == bound {
		other = spec.keys[1]
	}
	if members[other].(*fakeMember).dials.Load() != map[string]int32{spec.keys[0]: before0, spec.keys[1]: before1}[other] {
		t.Fatal("post binding changed to another hop")
	}
}

type metadataTestMember struct {
	*fakeMember
	dial func(context.Context, *C.Metadata) (C.Conn, error)
}

func (m *metadataTestMember) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	m.dials.Add(1)
	return m.dial(ctx, metadata)
}

func TestPostHopQuarantineRebindsAcrossNodeAdapters(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{
		"http://user:password@one.invalid:8080",
		"http://user:password@two.invalid:8080",
	}, 0)
	now := time.Unix(400, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(7, 11)})
	for range 3 {
		p.RecordHard(spec.keys[0])
	}
	binder := &testSiteBinder{values: map[string]string{"alice\x00example.test": spec.keys[0]}}
	members := map[string]member{
		spec.keys[0]: &fakeMember{name: "credentialed-hop"},
		spec.keys[1]: &fakeMember{name: "credentialed-hop"},
	}
	carrier := &nodeHealth{now: func() time.Time { return now }}
	first := newHopPoolAdapter("post-node-one", postHopPool, spec, members, p, binder, carrier, func() time.Time { return now })
	metadata := &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443}
	if _, err := first.DialContext(context.Background(), metadata); err != nil {
		t.Fatalf("healthy replacement dial failed: %v", err)
	}
	bound, ok := binder.Get("alice", "example.test")
	if !ok || bound != spec.keys[1] {
		t.Fatal("first adapter did not replace the quarantined binding with the healthy hop")
	}
	before := members[bound].(*fakeMember).dials.Load()
	second := newHopPoolAdapter("post-node-two", postHopPool, spec, members, p, binder, carrier, func() time.Time { return now })
	if _, err := second.DialContext(context.Background(), metadata); err != nil {
		t.Fatalf("rebound dial failed: %v", err)
	}
	if got := members[bound].(*fakeMember).dials.Load(); got != before+1 {
		t.Fatal("second node adapter did not reuse the shared site binding")
	}
	if got := members[spec.keys[0]].(*fakeMember).dials.Load(); got != 0 {
		t.Fatalf("quarantined hop was dialed %d times", got)
	}
	if got := poolState(p, spec.keys[0]); got.State != pool.Quarantined || got.Trips != 1 {
		t.Fatalf("shared quarantine state changed: %#v", got)
	}
}

func TestPostHopAllQuarantinedTriesOneMemberAndDoesNotBypass(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{
		"http://user:password@one.invalid:8080",
		"http://user:password@two.invalid:8080",
	}, 0)
	now := time.Unix(500, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(17, 19)})
	for _, key := range spec.keys {
		for range 3 {
			p.RecordHard(key)
		}
	}
	members := make(map[string]member, len(spec.keys))
	for i, key := range spec.keys {
		members[key] = &fakeMember{
			name: "credentialed-hop",
			err:  fmt.Errorf("%s connect error: connection refused", hopAddress(spec.uris[i])),
		}
	}
	adapter := newHopPoolAdapter("post-all-quarantined", postHopPool, spec, members, p, nil, nil, func() time.Time { return now })
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
	if err == nil || !isHopPoolDialError(err) || strings.Contains(err.Error(), "password") {
		t.Fatal("all-quarantined post dial did not return a sanitized hop-pool failure")
	}
	var attempts int32
	for _, member := range members {
		attempts += member.(*fakeMember).dials.Load()
	}
	if attempts != 1 {
		t.Fatalf("post pool attempted %d members, want exactly one", attempts)
	}
}

func TestPostHopDeadCarrierFailureDoesNotChangeHopHealth(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{
		"http://user:password@one.invalid:8080",
		"http://user:password@two.invalid:8080",
	}, 0)
	now := time.Unix(600, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(23, 29)})
	members := make(map[string]member, len(spec.keys))
	for i, key := range spec.keys {
		members[key] = &fakeMember{
			name: "credentialed-hop",
			err:  fmt.Errorf("%s connect error: connection refused", hopAddress(spec.uris[i])),
		}
	}
	adapter := newHopPoolAdapter("post-dead-carrier", postHopPool, spec, members, p, nil, nil, func() time.Time { return now })
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
	if err == nil || !isHopPoolDialError(err) || strings.Contains(err.Error(), "password") {
		t.Fatal("dead-carrier post failure was not safely wrapped")
	}
	for _, key := range spec.keys {
		if got := poolState(p, key); got.HardFailures != 0 || got.Sand != 0 || got.State != pool.Healthy {
			t.Fatalf("dead carrier changed hop health: %#v", got)
		}
	}
}

func TestPostHopCarrierConnectErrorSurvivesPoolFailure(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{
		"http://user:password@one.invalid:8080",
		"http://user:password@two.invalid:8080",
	}, 0)
	now := time.Unix(700, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(31, 37)})
	carrierErr := errors.New("carrier sentinel")
	members := map[string]member{
		spec.keys[0]: &fakeMember{name: "credentialed-hop", err: fmt.Errorf("node.invalid:443 connect error: %w", carrierErr)},
		spec.keys[1]: &fakeMember{name: "credentialed-hop", err: fmt.Errorf("node.invalid:443 connect error: %w", carrierErr)},
	}
	adapter := newHopPoolAdapter("post-carrier-error", postHopPool, spec, members, p, nil, nil, func() time.Time { return now })
	for _, hop := range adapter.hops {
		hop.nextAddress = "node.invalid:443"
	}
	_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
	if err == nil || isHopPoolDialError(err) || !errors.Is(err, carrierErr) {
		t.Fatal("carrier connect error was not preserved outside hop-pool classification")
	}
	var carrier *poolCarrierError
	if !errors.As(err, &carrier) {
		t.Fatal("carrier connect error lost its carrier classification")
	}
	if strings.Contains(err.Error(), "password") {
		t.Fatal("carrier error leaked hop credentials")
	}
	for _, key := range spec.keys {
		if got := poolState(p, key); got.HardFailures != 0 || got.Sand != 0 || got.State != pool.Healthy {
			t.Fatalf("carrier error changed hop health: %#v", got)
		}
	}
}

func TestPostHopHealthCountsStayBoundedWithFakeMember(t *testing.T) {
	spec := newHopPoolSpec(postHopPool, []string{"http://user:password@one.invalid:8080"}, 0)
	now := time.Unix(800, 0)
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(41, 43)})
	fake := &fakeMember{name: "credentialed-hop", err: fmt.Errorf("%s connect error: connection refused", hopAddress(spec.uris[0]))}
	carrier := &nodeHealth{now: func() time.Time { return now }}
	carrier.recordProbe(true, time.Millisecond)
	adapter := newHopPoolAdapter("post-bounded-health", postHopPool, spec, map[string]member{spec.keys[0]: fake}, p, nil, carrier, func() time.Time { return now })
	for range 9 {
		_, err := adapter.DialContext(context.Background(), &C.Metadata{NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443})
		if err == nil || !isHopPoolDialError(err) || strings.Contains(err.Error(), "password") {
			t.Fatal("post failure did not remain sanitized")
		}
	}
	got := poolState(p, spec.keys[0])
	if got.HardFailures != 3 || got.Trips != 1 || got.State != pool.Quarantined {
		t.Fatalf("post health count exceeded or missed quarantine bound: %#v", got)
	}
	if attempts := fake.dials.Load(); attempts != 9 {
		t.Fatalf("fake member dial count = %d, want one per post request", attempts)
	}
}

func TestHopPoolMetadataIsClonedPerAttempt(t *testing.T) {
	spec := newHopPoolSpec(preHopPool, []string{
		"socks5://user:password@one.invalid:1080",
		"socks5://user:password@two.invalid:1080",
	}, 0)
	now := time.Unix(300, 0)
	seed := [2]uint64{13, 21}
	p := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])})
	order := pool.New(spec.keys, pool.Options{Clock: func() time.Time { return now }, RNG: rand.NewPCG(seed[0], seed[1])}).Order(now)
	if len(order) != 2 {
		t.Fatalf("pool order has %d keys, want two", len(order))
	}
	addresses := make(map[string]string, len(spec.keys))
	for i, key := range spec.keys {
		addresses[key] = hopAddress(spec.uris[i])
	}
	original := &C.Metadata{
		NetWork: C.TCP, InUser: "alice", Host: "www.example.test", DstPort: 443,
		SrcGeoIP: []string{"original-source"}, DstGeoIP: []string{"original-destination"},
	}
	var calls []string
	var received []*C.Metadata
	members := make(map[string]member, len(spec.keys))
	for _, key := range spec.keys {
		key := key
		members[key] = &metadataTestMember{
			fakeMember: &fakeMember{name: "metadata-member"},
			dial: func(_ context.Context, metadata *C.Metadata) (C.Conn, error) {
				calls = append(calls, key)
				received = append(received, metadata)
				if metadata.Host != original.Host || metadata.DstPort != original.DstPort ||
					metadata.SrcGeoIP[0] != "original-source" || metadata.DstGeoIP[0] != "original-destination" {
					t.Errorf("attempt %q received metadata changed by an earlier attempt", key)
				}
				metadata.Host = "mutated.example.test"
				metadata.SrcGeoIP[0] = "mutated-source"
				metadata.DstGeoIP[0] = "mutated-destination"
				if key == order[0] {
					return nil, fmt.Errorf("%s connect error: connection refused", addresses[key])
				}
				return &fakeConn{}, nil
			},
		}
	}
	adapter := newHopPoolAdapter("metadata-test", preHopPool, spec, members, p, nil, nil, func() time.Time { return now })
	conn, err := adapter.DialContext(context.Background(), original)
	if err != nil || conn == nil {
		t.Fatal("pool did not fail over after the first metadata-mutating attempt")
	}
	if len(calls) != 2 || calls[0] != order[0] || calls[1] != order[1] {
		t.Fatalf("attempt order = %v, want %v", calls, order)
	}
	if len(received) != 2 || received[0] == received[1] {
		t.Fatal("attempts did not receive independent metadata clones")
	}
	if original.Host != "www.example.test" || original.SrcGeoIP[0] != "original-source" || original.DstGeoIP[0] != "original-destination" {
		t.Fatal("hop attempts mutated caller metadata")
	}
}
