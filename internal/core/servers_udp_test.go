package core

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

var udpTestProtocolID atomic.Uint64

type serverTestProtocol struct {
	name         string
	udp          bool
	mu           sync.Mutex
	events       []string
	failRevision string
	sentinel     error
	owned        map[string]string
}

func (p *serverTestProtocol) Name() string                                           { return p.name }
func (*serverTestProtocol) SingleUser() bool                                         { return false }
func (*serverTestProtocol) Validate(map[string]string) error                         { return nil }
func (*serverTestProtocol) NewMaterial(map[string]string) (map[string]string, error) { return nil, nil }
func (*serverTestProtocol) NewSecret(string, map[string]string) (map[string]string, error) {
	return nil, nil
}
func (*serverTestProtocol) Link(servers.Spec, string, string) (string, error) { return "", nil }
func (p *serverTestProtocol) UDPBound() bool                                  { return p.udp }
func (p *serverTestProtocol) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}
func (p *serverTestProtocol) Start(sp servers.Spec, _ servers.Env) (io.Closer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "start:"+sp.Name)
	if sp.Params["revision"] == p.failRevision && p.failRevision != "" {
		return nil, p.sentinel
	}
	if p.owned == nil {
		p.owned = map[string]string{}
	}
	key := fmt.Sprintf("%s/%d", sp.Bind, sp.Port)
	if owner := p.owned[key]; owner != "" {
		return nil, errors.New("fake endpoint owned by " + owner)
	}
	p.owned[key] = sp.Name
	return &serverTestCloser{p: p, name: sp.Name, key: key}, nil
}

type serverTestCloser struct {
	p         *serverTestProtocol
	name, key string
	once      sync.Once
}

func (c *serverTestCloser) Close() error {
	c.once.Do(func() {
		c.p.mu.Lock()
		defer c.p.mu.Unlock()
		c.p.events = append(c.p.events, "close:"+c.name)
		if c.p.owned[c.key] == c.name {
			delete(c.p.owned, c.key)
		}
	})
	return nil
}
func newServerTestProtocol(udp bool) *serverTestProtocol {
	p := &serverTestProtocol{name: fmt.Sprintf("core-test-%d", udpTestProtocolID.Add(1)), udp: udp, sentinel: errors.New("injected start failure")}
	servers.Register(p)
	return p
}
func testServerSpec(name, protocol string, port int) servers.Spec {
	return servers.Spec{Name: name, Protocol: protocol, Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: map[string]string{}}
}
func testServerSet() *serverSet { return &serverSet{broker: newSocketBroker()} }
func TestServerUDPPrepareCommitTransaction(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	defer s.close()
	old := testServerSpec("entry", p.Name(), 18001)
	tx, err := s.prepare([]servers.Spec{old}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.snapshot(); len(got) != 0 {
		t.Fatalf("started during prepare: %v", got)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	if got := p.snapshot(); fmt.Sprint(got) != "[start:entry]" {
		t.Fatalf("events %v", got)
	}
	entry := s.items["entry"]
	tx, err = s.prepare([]servers.Spec{old}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	if s.items["entry"] != entry {
		t.Fatal("equal spec replaced")
	}
	changed := old.Clone()
	changed.Params["revision"] = "changed"
	before := p.snapshot()
	tx, err = s.prepare([]servers.Spec{changed}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.snapshot(); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("prepare started UDP: before %v after %v", before, got)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	if got := p.snapshot(); !orderedContains(got, []string{"close:entry", "start:entry"}) {
		t.Fatalf("replacement events %v", got)
	}
	if s.items["entry"].counters != entry.counters {
		t.Fatal("replacement lost counters")
	}
	tx, err = s.prepare(nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	got := p.snapshot()
	if got[len(got)-1] != "close:entry" {
		t.Fatalf("remove events %v", got)
	}
}
func TestServerUDPInitialCommitFailureCleansEarlierStart(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	defer s.close()
	a := testServerSpec("a", p.Name(), 18051)
	b := testServerSpec("b", p.Name(), 18052)
	b.Params["revision"] = "fail"
	p.failRevision = "fail"
	tx, err := s.prepare([]servers.Spec{a, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); !errors.Is(err, p.sentinel) {
		t.Fatalf("commit error = %v", err)
	}
	if len(s.items) != 0 {
		t.Fatalf("partially committed entries: %v", s.items)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.owned) != 0 {
		t.Fatalf("leaked fake ownership: %v", p.owned)
	}
}

func TestServerUDPFailureRestoresRemovedEntries(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	defer s.close()
	a := testServerSpec("a", p.Name(), 18061)
	b := testServerSpec("b", p.Name(), 18062)
	tx, err := s.prepare([]servers.Spec{a, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	oldA, oldB := s.items["a"], s.items["b"]
	na := a.Clone()
	na.Params["revision"] = "good"
	fail := testServerSpec("c", p.Name(), 18063)
	fail.Params["revision"] = "fail"
	p.failRevision = "fail"
	tx, err = s.prepare([]servers.Spec{na, fail}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); !errors.Is(err, p.sentinel) {
		t.Fatalf("commit error = %v", err)
	}
	if s.items["a"] != oldA || s.items["b"] != oldB || s.items["c"] != nil {
		t.Fatal("did not restore replaced and removed entries")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owned["127.0.0.1/18061"] != "a" || p.owned["127.0.0.1/18062"] != "b" {
		t.Fatalf("ownership not restored: %v", p.owned)
	}
}

func TestServerUDPCommitFailureRestoresOwnership(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	defer s.close()
	a := testServerSpec("a", p.Name(), 18011)
	b := testServerSpec("b", p.Name(), 18012)
	tx, err := s.prepare([]servers.Spec{a, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	oldA, oldB := s.items["a"], s.items["b"]
	na := a.Clone()
	na.Params["revision"] = "good"
	b = b.Clone()
	b.Params["revision"] = "fail"
	p.mu.Lock()
	p.events = nil
	p.failRevision = "fail"
	sentinel := p.sentinel
	p.mu.Unlock()
	tx, err = s.prepare([]servers.Spec{na, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); !errors.Is(err, sentinel) {
		t.Fatalf("error identity: %v", err)
	}
	tx.rollback()
	if s.items["a"] != oldA || s.items["b"] != oldB {
		t.Fatal("old entries not restored")
	}
	gotEvents := p.snapshot()
	wantEvents := []string{"close:a", "close:b", "start:a", "start:b", "close:a", "start:a", "start:b"}
	if len(gotEvents) != len(wantEvents) {
		t.Fatalf("rollback events %v, want %v", gotEvents, wantEvents)
	}
	for i := range wantEvents {
		if gotEvents[i] != wantEvents[i] {
			t.Fatalf("rollback events %v, want %v", gotEvents, wantEvents)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owned["127.0.0.1/18011"] != "a" || p.owned["127.0.0.1/18012"] != "b" {
		t.Fatalf("ownership not restored: %v", p.owned)
	}
}
func TestServerUDPCloseReleasesOwnership(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	tx, err := s.prepare([]servers.Spec{testServerSpec("entry", p.Name(), 18021)}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	s.close()
	if len(s.items) != 0 {
		t.Fatalf("server entries remain after close: %v", s.items)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.owned) != 0 {
		t.Fatalf("ownership remains after close: %v", p.owned)
	}
}

func TestServerUDPPortInUse(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	defer s.close()
	a := testServerSpec("a", p.Name(), 18031)
	b := testServerSpec("b", p.Name(), 18032)
	tx, err := s.prepare([]servers.Spec{a, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	na := a.Clone()
	na.Params["revision"] = "changed"
	b.Params["revision"] = "fail"
	p.mu.Lock()
	p.failRevision = "fail"
	p.sentinel = syscall.EADDRINUSE
	if runtime.GOOS == "windows" {
		p.sentinel = syscall.Errno(10048)
	}
	p.mu.Unlock()
	tx, err = s.prepare([]servers.Spec{na, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.commit()
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" {
		t.Fatalf("error = %v, want PORT_IN_USE", err)
	}
	if !strings.Contains(err.Error(), "previous servers restored") {
		t.Fatalf("error = %v, want restoration message", err)
	}
}

func TestServerEndpointsConflictPerTransport(t *testing.T) {
	u := newServerTestProtocol(true)
	tcp := newServerTestProtocol(false)
	s := testServerSet()
	defer s.close()
	x := testServerSpec("u", u.Name(), 18021)
	y := testServerSpec("t", tcp.Name(), 18021)
	tx, err := s.prepare([]servers.Spec{x, y}, nil, nil, nil)
	if err != nil {
		t.Fatalf("cross-transport conflict: %v", err)
	}
	tx.rollback()
	y = testServerSpec("u2", u.Name(), 18021)
	if _, err = s.prepare([]servers.Spec{x, y}, nil, nil, nil); err == nil {
		t.Fatal("same transport conflict accepted")
	}
}
func TestServerUDPPrepareRollbackDoesNotStart(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	defer s.close()
	tx, err := s.prepare([]servers.Spec{testServerSpec("entry", p.Name(), 18031)}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.rollback()
	if got := p.snapshot(); len(got) != 0 {
		t.Fatalf("rollback events %v", got)
	}
}
func TestServerUDPReplacementsAreSorted(t *testing.T) {
	p := newServerTestProtocol(true)
	s := testServerSet()
	defer s.close()
	a := testServerSpec("zeta", p.Name(), 18041)
	b := testServerSpec("alpha", p.Name(), 18042)
	tx, err := s.prepare([]servers.Spec{a, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.events = nil
	p.mu.Unlock()
	a.Params["revision"] = "1"
	b.Params["revision"] = "1"
	tx, err = s.prepare([]servers.Spec{a, b}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.commit(); err != nil {
		t.Fatal(err)
	}
	want := []string{"close:alpha", "close:zeta", "start:alpha", "start:zeta"}
	got := p.snapshot()
	if len(got) != len(want) {
		t.Fatalf("events %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
}
func orderedContains(have, want []string) bool {
	at := 0
	for _, v := range have {
		if at < len(want) && v == want[at] {
			at++
		}
	}
	return at == len(want)
}
