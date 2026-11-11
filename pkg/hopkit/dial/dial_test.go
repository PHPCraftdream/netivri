package dial

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"sync"
	"testing"
	"time"

	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

var testNow = time.Unix(1_700_000_000, 0).UTC()

func newPool(keys ...string) *pool.Pool {
	return pool.New(keys, pool.Options{Clock: func() time.Time { return testNow }, RNG: rand.NewPCG(91, 17)})
}

type fakeHop struct {
	key  string
	dial func(context.Context, string, string) (net.Conn, error)
}

func (h *fakeHop) Key() string { return h.key }
func (h *fakeHop) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return h.dial(ctx, network, address)
}

type fakeConn struct {
	closed bool
	mu     sync.Mutex
}

func (c *fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *fakeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *fakeConn) Close() error                     { c.mu.Lock(); c.closed = true; c.mu.Unlock(); return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (c *fakeConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "memory" }
func (a fakeAddr) String() string  { return string(a) }

func deterministicOrder(keys []string, configure func(*pool.Pool)) []string {
	p := newPool(keys...)
	if configure != nil {
		configure(p)
	}
	return p.Order(testNow)
}

func TestPreDialSkipsQuarantineRetriesUniqueOrderAndRecordsSuccess(t *testing.T) {
	keys := []string{"a", "b", "c"}
	quarantine := func(p *pool.Pool) {
		for i := 0; i < 3; i++ {
			p.RecordHard("a")
		}
	}
	want := deterministicOrder(keys, quarantine)
	if len(want) != 2 {
		t.Fatalf("healthy order = %v, want two non-quarantined hops", want)
	}
	attempts := make([]string, 0, 2)
	hops := make([]Hop, 0, len(keys))
	for _, key := range keys {
		key := key
		hops = append(hops, &fakeHop{key: key, dial: func(context.Context, string, string) (net.Conn, error) {
			attempts = append(attempts, key)
			if key == want[0] {
				return nil, &StageError{Stage: Connect, Err: errors.New("private failure")}
			}
			return &fakeConn{}, nil
		}})
	}
	p := newPool(keys...)
	quarantine(p)
	gotConn, err := New(p, hops, Options{}).DialContext(context.Background(), "tcp", "target:443")
	if err != nil || gotConn == nil {
		t.Fatalf("DialContext = (%v, %v), want success", gotConn, err)
	}
	if len(attempts) != 2 || attempts[0] != want[0] || attempts[1] != want[1] {
		t.Fatalf("attempt order = %v, want %v", attempts, want)
	}
	for _, snapshot := range p.Snapshot() {
		switch snapshot.Key {
		case "a":
			if snapshot.State != pool.Quarantined {
				t.Errorf("quarantined hop state = %q", snapshot.State)
			}
		case want[0]:
			if snapshot.HardFailures != 1 {
				t.Errorf("failed hop hard failures = %d, want 1", snapshot.HardFailures)
			}
		case want[1]:
			if snapshot.HardFailures != 0 {
				t.Errorf("successful hop hard failures = %d, want 0", snapshot.HardFailures)
			}
		}
	}
}

func TestPreDialAllDeadTriesEveryHopOnceAndReturnsFinalErrorUnchanged(t *testing.T) {
	keys := []string{"a", "b", "c"}
	p := newPool(keys...)
	for _, key := range keys {
		for i := 0; i < 3; i++ {
			p.RecordHard(key)
		}
	}
	want := deterministicOrder(keys, func(mirror *pool.Pool) {
		for _, key := range keys {
			for i := 0; i < 3; i++ {
				mirror.RecordHard(key)
			}
		}
	})
	attempts := make([]string, 0, len(keys))
	final := &StageError{Stage: Unknown, Err: errors.New("opaque")}
	hops := make([]Hop, 0, len(keys))
	for _, key := range keys {
		key := key
		hops = append(hops, &fakeHop{key: key, dial: func(context.Context, string, string) (net.Conn, error) {
			attempts = append(attempts, key)
			return nil, final
		}})
	}
	_, err := New(p, hops, Options{}).DialContext(context.Background(), "tcp", "target:443")
	if err != final {
		t.Fatalf("final error = %v, want original StageError", err)
	}
	if len(attempts) != len(keys) {
		t.Fatalf("attempts = %v, want each hop once", attempts)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Errorf("attempts = %v, want order %v", attempts, want)
			break
		}
	}
}

type fakeBinder struct {
	values  map[string]string
	unbound []string
	bound   []string
}

func binderKey(login, site string) string { return login + "\x00" + site }
func (b *fakeBinder) Get(login, site string) (string, bool) {
	key, ok := b.values[binderKey(login, site)]
	return key, ok
}
func (b *fakeBinder) Bind(login, site, key string) {
	b.values[binderKey(login, site)] = key
	b.bound = append(b.bound, key)
}
func (b *fakeBinder) Unbind(login, site string) {
	b.unbound = append(b.unbound, binderKey(login, site))
	delete(b.values, binderKey(login, site))
}
func (b *fakeBinder) Forget(key string) int {
	count := 0
	for binding, value := range b.values {
		if value == key {
			delete(b.values, binding)
			count++
		}
	}
	return count
}

func TestPostDialHealthyBindingWins(t *testing.T) {
	p := newPool("a", "b", "c")
	binder := &fakeBinder{values: map[string]string{binderKey("login", "site"): "c"}}
	calls := []string{}
	hops := make([]Hop, 0, 3)
	for _, key := range []string{"a", "b", "c"} {
		key := key
		hops = append(hops, &fakeHop{key: key, dial: func(context.Context, string, string) (net.Conn, error) {
			calls = append(calls, key)
			return &fakeConn{}, nil
		}})
	}
	_, err := New(p, hops, Options{Post: true, Binder: binder}).DialSite(context.Background(), "tcp", "target:443", "login", "site")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "c" {
		t.Fatalf("calls = %v, want bound hop c only", calls)
	}
	if len(binder.bound) != 1 || binder.bound[0] != "c" {
		t.Fatalf("rebound = %v, want c", binder.bound)
	}
}

func TestPostDialUnbindsQuarantinedBindingAndRebindsHealthyChoice(t *testing.T) {
	keys := []string{"a", "b", "c"}
	p := newPool(keys...)
	for i := 0; i < 3; i++ {
		p.RecordHard("a")
	}
	want := deterministicOrder(keys, func(mirror *pool.Pool) {
		for i := 0; i < 3; i++ {
			mirror.RecordHard("a")
		}
	})
	binder := &fakeBinder{values: map[string]string{binderKey("login", "site"): "a"}}
	calls := []string{}
	hops := make([]Hop, 0, 3)
	for _, key := range keys {
		key := key
		hops = append(hops, &fakeHop{key: key, dial: func(context.Context, string, string) (net.Conn, error) {
			calls = append(calls, key)
			return &fakeConn{}, nil
		}})
	}
	_, err := New(p, hops, Options{Post: true, Binder: binder}).DialSite(context.Background(), "tcp", "target:443", "login", "site")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != want[0] || calls[0] == "a" {
		t.Fatalf("calls = %v, want first healthy choice %s", calls, want[0])
	}
	if len(binder.unbound) != 1 || len(binder.bound) != 1 || binder.bound[0] != calls[0] {
		t.Fatalf("binding changes: unbound=%v rebound=%v", binder.unbound, binder.bound)
	}
}

func TestPostDialAllDeadNeverRetriesOrBypasses(t *testing.T) {
	keys := []string{"a", "b", "c"}
	p := newPool(keys...)
	for _, key := range keys {
		for i := 0; i < 3; i++ {
			p.RecordHard(key)
		}
	}
	want := deterministicOrder(keys, func(mirror *pool.Pool) {
		for _, key := range keys {
			for i := 0; i < 3; i++ {
				mirror.RecordHard(key)
			}
		}
	})[0]
	calls := []string{}
	final := &StageError{Stage: Connect, Err: errors.New("not available")}
	hops := make([]Hop, 0, 3)
	for _, key := range keys {
		key := key
		hops = append(hops, &fakeHop{key: key, dial: func(context.Context, string, string) (net.Conn, error) {
			calls = append(calls, key)
			return nil, final
		}})
	}
	conn, err := New(p, hops, Options{Post: true}).DialSite(context.Background(), "tcp", "target:443", "login", "site")
	if conn != nil || err != final {
		t.Fatalf("post dial = (%v, %v), want original failure", conn, err)
	}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls = %v, want exactly selected quarantined hop %q", calls, want)
	}
}

func TestNilConnectionDoesNotReviveQuarantinedHop(t *testing.T) {
	p := newPool("hop")
	for i := 0; i < 3; i++ {
		p.RecordHard("hop")
	}
	hop := &fakeHop{key: "hop", dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, nil
	}}
	conn, err := New(p, []Hop{hop}, Options{}).DialContext(context.Background(), "tcp", "target:443")
	if conn != nil || err != ErrNoConnection {
		t.Fatalf("nil connection result = (%v, %v), want nil and ErrNoConnection", conn, err)
	}
	snapshot := p.Snapshot()[0]
	if snapshot.State != pool.Quarantined || snapshot.HardFailures != 3 {
		t.Fatalf("nil connection revived quarantined hop: %#v", snapshot)
	}
}

func TestNilConnectionRetriesPreButDoesNotBindPost(t *testing.T) {
	keys := []string{"a", "b"}
	want := deterministicOrder(keys, nil)
	p := newPool(keys...)
	calls := []string{}
	hops := make([]Hop, 0, len(keys))
	for _, key := range keys {
		key := key
		hops = append(hops, &fakeHop{key: key, dial: func(context.Context, string, string) (net.Conn, error) {
			calls = append(calls, key)
			if key == want[0] {
				return nil, nil
			}
			return &fakeConn{}, nil
		}})
	}
	conn, err := New(p, hops, Options{}).DialContext(context.Background(), "tcp", "target:443")
	if err != nil || conn == nil {
		t.Fatalf("pre dial = (%v, %v), want later candidate success", conn, err)
	}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("pre attempts = %v, want %v", calls, want)
	}

	postBinder := &fakeBinder{values: make(map[string]string)}
	postCalls := []string{}
	postHops := make([]Hop, 0, len(keys))
	for _, key := range keys {
		key := key
		postHops = append(postHops, &fakeHop{key: key, dial: func(context.Context, string, string) (net.Conn, error) {
			postCalls = append(postCalls, key)
			return nil, nil
		}})
	}
	postConn, postErr := New(newPool(keys...), postHops, Options{Post: true, Binder: postBinder}).DialSite(context.Background(), "tcp", "target:443", "login", "site")
	if postConn != nil || postErr != ErrNoConnection {
		t.Fatalf("post nil connection result = (%v, %v), want ErrNoConnection", postConn, postErr)
	}
	if len(postCalls) != 1 || len(postBinder.bound) != 0 {
		t.Fatalf("post calls=%v bindings=%v; want one call and no binding", postCalls, postBinder.bound)
	}
}

func TestStageErrorsAreSanitizedUnwrappedAndAttributedOnlyWhenKnown(t *testing.T) {
	secret := errors.New("secret user:password@proxy.invalid")
	staged := &StageError{Stage: Connect, Err: secret}
	if got := staged.Error(); got == "" || contains(got, "secret") || contains(got, "password") || !errors.Is(staged, secret) {
		t.Fatalf("sanitized error = %q; unwrap=%v", got, errors.Is(staged, secret))
	}
	for _, test := range []struct {
		name string
		err  error
		hard int
		soft float64
	}{
		{name: "connect", err: staged, hard: 1},
		{name: "ambiguous handshake", err: &StageError{Stage: Handshake}, soft: 1},
		{name: "refusal", err: &StageError{Stage: Refusal}, soft: 1},
		{name: "next unreachable target", err: &StageError{Stage: Target, NextUnreachable: true}},
		{name: "unknown stage", err: &StageError{Stage: Unknown}},
		{name: "unstructured", err: secret},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := newPool("hop")
			hop := &fakeHop{key: "hop", dial: func(context.Context, string, string) (net.Conn, error) { return nil, test.err }}
			_, gotErr := New(p, []Hop{hop}, Options{}).DialContext(context.Background(), "tcp", "target:443")
			if gotErr != test.err {
				t.Fatalf("returned error %v, want original %v", gotErr, test.err)
			}
			snapshot := p.Snapshot()[0]
			if snapshot.HardFailures != test.hard || snapshot.Sand != test.soft {
				t.Fatalf("health = hard %d sand %v; want hard %d sand %v", snapshot.HardFailures, snapshot.Sand, test.hard, test.soft)
			}
		})
	}
}

func TestCanceledDialNeverChangesHealth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newPool("hop")
	hop := &fakeHop{key: "hop", dial: func(context.Context, string, string) (net.Conn, error) {
		cancel()
		return nil, &StageError{Stage: Connect, Err: context.Canceled}
	}}
	_, err := New(p, []Hop{hop}, Options{}).DialContext(ctx, "tcp", "target:443")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if got := p.Snapshot()[0]; got.HardFailures != 0 || got.Sand != 0 {
		t.Fatalf("canceled dial changed health: %#v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
