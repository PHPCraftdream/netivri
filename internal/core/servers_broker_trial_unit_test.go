package core

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/metacubex/sing-shadowsocks/shadowaead_2022"
)

type brokerTestTrial struct {
	min     int
	verdict servers.Verdict
	key     []byte
	calls   int
}

func (t *brokerTestTrial) MinBytes() int { return t.min }
func (t *brokerTestTrial) Try([]byte, time.Time) (servers.Verdict, []byte) {
	t.calls++
	return t.verdict, t.key
}

type brokerTestConn struct {
	input         []byte
	closed        bool
	reads, writes int
}

func (c *brokerTestConn) Read(p []byte) (int, error) {
	c.reads++
	if len(c.input) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.input)
	c.input = c.input[n:]
	return n, nil
}
func (c *brokerTestConn) Write(p []byte) (int, error)    { c.writes++; return len(p), nil }
func (c *brokerTestConn) Close() error                   { c.closed = true; return nil }
func (*brokerTestConn) LocalAddr() net.Addr              { return unitAddr("local") }
func (*brokerTestConn) RemoteAddr() net.Addr             { return unitAddr("127.0.0.1:1") }
func (*brokerTestConn) SetDeadline(time.Time) error      { return nil }
func (*brokerTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*brokerTestConn) SetWriteDeadline(time.Time) error { return nil }

type brokerDeadlineCountConn struct {
	brokerTestConn
	mu            sync.Mutex
	readDeadlines []time.Time
	setDeadlines  []time.Time
	readStarted   chan struct{}
	peerEOF       chan struct{}
	closedSignal  chan struct{}
}

func newBrokerDeadlineCountConn(input []byte) *brokerDeadlineCountConn {
	return &brokerDeadlineCountConn{
		brokerTestConn: brokerTestConn{input: append([]byte(nil), input...)},
		readStarted:    make(chan struct{}), peerEOF: make(chan struct{}), closedSignal: make(chan struct{}),
	}
}

func (c *brokerDeadlineCountConn) SetReadDeadline(d time.Time) error {
	c.mu.Lock()
	c.readDeadlines = append(c.readDeadlines, d)
	c.mu.Unlock()
	return nil
}

func (c *brokerDeadlineCountConn) SetDeadline(d time.Time) error {
	c.mu.Lock()
	c.setDeadlines = append(c.setDeadlines, d)
	c.mu.Unlock()
	return nil
}

func (c *brokerDeadlineCountConn) Read(p []byte) (int, error) {
	if len(c.input) > 0 {
		return c.brokerTestConn.Read(p)
	}
	select {
	case <-c.readStarted:
	default:
		close(c.readStarted)
	}
	<-c.peerEOF
	return 0, io.EOF
}

func (c *brokerDeadlineCountConn) Close() error {
	c.brokerTestConn.closed = true
	select {
	case <-c.closedSignal:
	default:
		close(c.closedSignal)
	}
	return nil
}

func seedClassifyEndpoint(b *socketBroker, leases ...*socketLease) *socketEndpoint {
	ep := &socketEndpoint{broker: b, key: endpointKey{"tcp", "test"}, leases: map[*socketLease]struct{}{}, members: map[string]*socketLease{}, changed: make(chan struct{}), pending: map[netip.Addr]int{}}
	b.endpoints[ep.key] = ep
	for _, l := range leases {
		l.endpoint = ep
		l.done = make(chan struct{})
		l.tcp = make(chan net.Conn, 1)
		ep.leases[l] = struct{}{}
		ep.members[l.name] = l
	}
	return ep
}

func TestBrokerDeadlineCountContract(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return time.Second }
	trial := &brokerTestTrial{min: 3, verdict: servers.Reject}
	l := &socketLease{name: "trial", trial: trial, spec: &servers.Spec{Protocol: "vmess"}}
	ep := seedClassifyEndpoint(b, l)
	fc := newBrokerDeadlineCountConn([]byte("abc"))
	// A failed assertion must not leave the drain goroutine blocked on the peer.
	t.Cleanup(func() {
		select {
		case <-fc.peerEOF:
		default:
			close(fc.peerEOF)
		}
	})
	done := make(chan struct{})
	go func() {
		b.classify(ep, &brokerConn{Conn: fc, broker: b}, []*socketLease{l})
		close(done)
	}()
	select {
	case <-fc.readStarted:
	case <-time.After(time.Second):
		t.Fatal("reject drain did not start")
	}
	fc.mu.Lock()
	readDeadlines := append([]time.Time(nil), fc.readDeadlines...)
	setDeadlines := append([]time.Time(nil), fc.setDeadlines...)
	fc.mu.Unlock()
	if len(readDeadlines) != 1 || readDeadlines[0].IsZero() {
		t.Fatalf("classification read deadlines=%v, want one nonzero deadline", readDeadlines)
	}
	if len(setDeadlines) != 1 || setDeadlines[0].IsZero() {
		t.Fatalf("drain deadlines=%v, want one nonzero deadline", setDeadlines)
	}
	if fc.writes != 0 || fc.closed {
		t.Fatalf("during drain writes=%d closed=%t, want no writes and open conn", fc.writes, fc.closed)
	}
	close(fc.peerEOF)
	select {
	case <-fc.closedSignal:
	case <-time.After(time.Second):
		t.Fatal("connection not closed after peer EOF")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("classification did not finish after peer EOF")
	}
	if fc.writes != 0 {
		t.Fatalf("reject writes=%d, want 0", fc.writes)
	}
}

func TestBrokerPendingReleaseNilMapAndTLSMembershipFromSpec(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	tlsSpec := servers.Spec{Name: "tls", Protocol: "trojan", Params: map[string]string{"sni": "one.example"}}
	tlsLease := &socketLease{name: "tls", spec: &tlsSpec, tcp: make(chan net.Conn, 1), done: make(chan struct{})}
	trial := &brokerTestTrial{min: 3, verdict: servers.Reject}
	trialLease := &socketLease{name: "trial", trial: trial, spec: &servers.Spec{Protocol: "vmess"}, tcp: make(chan net.Conn, 1), done: make(chan struct{})}
	ep := &socketEndpoint{broker: b, key: endpointKey{"tcp", "test"}, leases: map[*socketLease]struct{}{tlsLease: {}, trialLease: {}}, members: map[string]*socketLease{"tls": tlsLease, "trial": trialLease}, changed: make(chan struct{})}
	b.endpoints[ep.key] = ep
	tlsLease.endpoint = ep
	trialLease.endpoint = ep
	conn := &brokerTestConn{input: append([]byte(nil), multiportClientHello(t, "one.example", []string{"h2"})...)}
	b.classify(ep, &brokerConn{Conn: conn, broker: b}, []*socketLease{trialLease, tlsLease})
	select {
	case <-tlsLease.tcp:
	default:
		t.Fatal("TLS spec membership was not classified when lease.tls was unset")
	}
	if ep.pending != nil {
		t.Fatalf("nil pending map was initialized during release: %v", ep.pending)
	}
}

func TestBrokerRejectWithNilPendingMapDrainsAndReleases(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	trial := &brokerTestTrial{min: 3, verdict: servers.Reject}
	l := &socketLease{name: "trial", trial: trial, spec: &servers.Spec{Protocol: "vmess"}, tcp: make(chan net.Conn, 1), done: make(chan struct{})}
	ep := &socketEndpoint{broker: b, key: endpointKey{"tcp", "test"}, leases: map[*socketLease]struct{}{}, members: map[string]*socketLease{}, changed: make(chan struct{})}
	l.endpoint = ep
	ep.leases[l] = struct{}{}
	ep.members[l.name] = l
	conn := &brokerTestConn{input: []byte("abc")}
	b.classify(ep, &brokerConn{Conn: conn, broker: b}, []*socketLease{l})
	if ep.pending != nil {
		t.Fatalf("pending map initialized: %v", ep.pending)
	}
	if ep.unclassified.Load() != 1 || len(l.tcp) != 0 {
		t.Fatalf("unclassified=%d deliveries=%d", ep.unclassified.Load(), len(l.tcp))
	}
	if conn.writes != 0 || !conn.closed {
		t.Fatalf("reject writes=%d closed=%t", conn.writes, conn.closed)
	}
}

func TestBrokerPendingTimeoutRejectsAndReleases(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	trial := &brokerTestTrial{min: 3, verdict: servers.Reject}
	l := &socketLease{name: "trial", trial: trial, spec: &servers.Spec{Protocol: "vmess"}}
	ep := seedClassifyEndpoint(b, l)
	src := netip.MustParseAddr("127.0.0.1")
	ep.pending = map[netip.Addr]int{src: 1}
	c := &timeoutBrokerConn{}
	done := make(chan struct{})
	go func() { b.classify(ep, &brokerConn{Conn: c, broker: b}, []*socketLease{l}, src); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out classification did not finish")
	}
	if trial.calls != 0 || ep.unclassified.Load() != 1 || len(l.tcp) != 0 {
		t.Fatalf("calls=%d unclassified=%d dispatched=%d", trial.calls, ep.unclassified.Load(), len(l.tcp))
	}
	b.mu.Lock()
	pending := ep.pending[src]
	b.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending timeout slot retained: %d", pending)
	}
	if c.writes != 0 || !c.closed {
		t.Fatalf("rejected timeout writes=%d closed=%t", c.writes, c.closed)
	}
}

type timeoutBrokerConn struct{ brokerTestConn }

func (c *timeoutBrokerConn) Read([]byte) (int, error) { return 0, timeoutNetError{} }

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

type singleReadBrokerConn struct{ brokerTestConn }

func (c *singleReadBrokerConn) Read(p []byte) (int, error) {
	c.reads++
	if c.reads > 1 {
		return 0, io.EOF
	}
	n := copy(p, c.input)
	c.input = c.input[n:]
	return n, nil
}

func TestBrokerTLSWithTrialMembersDoesNotWaitOrPanic(t *testing.T) {
	hello := multiportClientHello(t, "one.example", []string{"h2"})
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	trial := &brokerTestTrial{min: 100, verdict: servers.Reject}
	sp := servers.Spec{Name: "tls", Protocol: "trojan", Params: map[string]string{"sni": "one.example"}}
	tlsLease := &socketLease{name: "tls", spec: &sp, tls: true}
	vm := &socketLease{name: "vm", spec: &servers.Spec{Protocol: "vmess"}, trial: trial}
	ep := seedClassifyEndpoint(b, tlsLease, vm)
	fake := &brokerTestConn{input: append([]byte(nil), hello...)}
	src := netip.MustParseAddr("127.0.0.1")
	ep.pending[src] = 1
	b.classify(ep, &brokerConn{Conn: fake, broker: b}, []*socketLease{vm, tlsLease}, src)
	select {
	case got := <-tlsLease.tcp:
		buf := make([]byte, len(hello))
		n, err := got.Read(buf)
		if err != nil || n != len(hello) {
			t.Fatalf("TLS replay length=%d err=%v", n, err)
		}
	case <-time.After(time.Second):
		t.Fatal("valid TLS classification did not dispatch promptly")
	}
	if trial.calls != 1 {
		t.Fatalf("trial calls=%d", trial.calls)
	}
}

func TestBrokerSyntheticSS2022TLSMarkerCollision(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	key := bytes.Repeat([]byte{0x42}, 16)
	password := base64.StdEncoding.EncodeToString(key)
	spec := servers.Spec{Name: "ss", Protocol: "shadowsocks", Accounts: []string{"u"}, Params: map[string]string{"cipher": "2022-blake3-aes-128-gcm"}, Users: servers.UserSecrets{"u": {"password": password}}}
	trial, err := servers.NewTrial(spec)
	if err != nil {
		t.Fatal(err)
	}
	salt := append([]byte{0x16, 3, 3}, bytes.Repeat([]byte{0x11}, 13)...)
	session := shadowaead_2022.SessionKey(key, salt, 16)
	block, err := aes.NewCipher(session)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	fixed := make([]byte, 11)
	fixed[0] = shadowaead_2022.HeaderTypeClient
	now := time.Now()
	binary.BigEndian.PutUint64(fixed[1:9], uint64(now.Unix()))
	flight := append(append([]byte(nil), salt...), aead.Seal(nil, make([]byte, aead.NonceSize()), fixed, nil)...)
	if len(flight) < trial.MinBytes() {
		t.Fatalf("flight length %d < min %d", len(flight), trial.MinBytes())
	}
	ssLease := &socketLease{name: "ss", spec: &spec, trial: trial}
	tlsSpec := servers.Spec{Name: "tls", Protocol: "trojan", Params: map[string]string{"sni": "one.example"}}
	tlsLease := &socketLease{name: "tls", spec: &tlsSpec, tls: true}
	ep := seedClassifyEndpoint(b, ssLease, tlsLease)
	conn := &singleReadBrokerConn{brokerTestConn: brokerTestConn{input: append([]byte(nil), flight...)}}
	src := netip.MustParseAddr("127.0.0.1")
	ep.pending[src] = 1
	b.classify(ep, &brokerConn{Conn: conn, broker: b}, []*socketLease{tlsLease, ssLease}, src)
	got := <-ssLease.tcp
	replay := make([]byte, len(flight))
	n, err := got.Read(replay)
	if err != nil || n != len(flight) || !bytes.Equal(replay, flight) {
		t.Fatalf("replay n=%d err=%v", n, err)
	}
	if conn.reads != 1 {
		t.Fatalf("front reads=%d", conn.reads)
	}
}

func TestBrokerTLSAndTrialCollisionChecksTrialOnce(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	hello := multiportClientHello(t, "one.example", []string{"h2"})
	trial := &brokerTestTrial{min: 3, verdict: servers.Reject}
	sp := servers.Spec{Name: "tls", Protocol: "trojan", Params: map[string]string{"sni": "one.example"}}
	tlsLease := &socketLease{name: "tls", spec: &sp, tls: true}
	vm := &socketLease{name: "vm", spec: &servers.Spec{Protocol: "vmess"}, trial: trial}
	ep := seedClassifyEndpoint(b, tlsLease, vm)
	fake := &brokerTestConn{input: append([]byte(nil), hello...)}
	src := netip.MustParseAddr("127.0.0.1")
	ep.pending[src] = 1
	b.classify(ep, &brokerConn{Conn: fake, broker: b}, []*socketLease{tlsLease, vm}, src)
	if trial.calls != 1 {
		t.Fatalf("trial calls=%d", trial.calls)
	}
	select {
	case <-tlsLease.tcp:
	default:
		t.Fatal("TLS lease did not receive connection")
	}
}

func TestBrokerTrialsTryOnceAndAcceptedPrefixReplay(t *testing.T) {
	for _, n := range []int{1, 41, 42, 142} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			b := newSocketBroker()
			b.drainWait = func() time.Duration { return 0 }
			tr := &brokerTestTrial{min: 42, verdict: servers.Reject}
			l := &socketLease{name: "vm", trial: tr, spec: &servers.Spec{Protocol: "vmess"}}
			ep := seedClassifyEndpoint(b, l)
			c := &blockingBrokerConn{data: make([]byte, n), entered: make(chan struct{}), release: make(chan struct{})}
			src := netip.MustParseAddr("127.0.0.1")
			ep.pending[src] = 1
			done := make(chan struct{})
			go func() { b.classify(ep, &brokerConn{Conn: c, broker: b}, []*socketLease{l}, src); close(done) }()
			<-c.entered
			if c.writes != 0 || c.closed {
				t.Fatalf("premature writes=%d closed=%t", c.writes, c.closed)
			}
			close(c.release)
			<-done
			if c.writes != 0 || !c.closed {
				t.Fatalf("final writes=%d closed=%t", c.writes, c.closed)
			}
			want := 0
			if n >= 42 {
				want = 1
			}
			if tr.calls != want {
				t.Fatalf("calls=%d want=%d", tr.calls, want)
			}
		})
	}
}

type blockingBrokerConn struct {
	data             []byte
	entered, release chan struct{}
	closed           bool
	writes           int
	once             sync.Once
}

func (c *blockingBrokerConn) Read(p []byte) (int, error) {
	if len(c.data) > 0 {
		n := copy(p, c.data)
		c.data = c.data[n:]
		return n, nil
	}
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return 0, io.EOF
}
func (c *blockingBrokerConn) Write(p []byte) (int, error)    { c.writes++; return len(p), nil }
func (c *blockingBrokerConn) Close() error                   { c.closed = true; return nil }
func (*blockingBrokerConn) LocalAddr() net.Addr              { return unitAddr("local") }
func (*blockingBrokerConn) RemoteAddr() net.Addr             { return unitAddr("127.0.0.1:1") }
func (*blockingBrokerConn) SetDeadline(time.Time) error      { return nil }
func (*blockingBrokerConn) SetReadDeadline(time.Time) error  { return nil }
func (*blockingBrokerConn) SetWriteDeadline(time.Time) error { return nil }

func TestBrokerPendingCapsAreEndpointAndSourceScoped(t *testing.T) {
	ep := &socketEndpoint{pending: map[netip.Addr]int{}}
	a := netip.MustParseAddr("192.0.2.1")
	b := netip.MustParseAddr("192.0.2.2")
	ep.pending[a] = 8
	if !(&socketBroker{}).pendingLimit(ep, a) {
		t.Fatal("source limit not enforced")
	}
	if (&socketBroker{}).pendingLimit(ep, b) {
		t.Fatal("source limit leaked")
	}
	ep.pending[a] = 63
	ep.pending[b] = 1
	if !(&socketBroker{}).pendingLimit(ep, b) {
		t.Fatal("endpoint limit not enforced")
	}
	other := &socketEndpoint{pending: map[netip.Addr]int{}}
	if (&socketBroker{}).pendingLimit(other, b) {
		t.Fatal("pending cap leaked across endpoints")
	}
}

func TestBrokerReplayCountsDuplicateAcceptedTrial(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	key := []byte("front-key")
	tr := &brokerTestTrial{min: 3, verdict: servers.Accept, key: key}
	l := &socketLease{name: "vm", trial: tr, spec: &servers.Spec{Protocol: "vmess"}}
	ep := seedClassifyEndpoint(b, l)
	for i := 0; i < 2; i++ {
		tr.calls = 0
		c := &brokerTestConn{input: []byte("abc")}
		src := netip.MustParseAddr("127.0.0.1")
		ep.pending[src] = 1
		b.classify(ep, &brokerConn{Conn: c, broker: b}, []*socketLease{l}, src)
		if tr.calls != 1 {
			t.Fatalf("trial calls=%d", tr.calls)
		}
		if i == 0 {
			select {
			case <-l.tcp:
			default:
				t.Fatal("first authentication not dispatched")
			}
		} else if ep.replayed.Load() != 1 || len(l.tcp) != 0 {
			t.Fatalf("replayed=%d deliveries=%d", ep.replayed.Load(), len(l.tcp))
		}
	}
}

func TestBrokerDrainHoldsConnectionUntilPeerEOFAndReleasesPending(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return time.Second }
	tr := &brokerTestTrial{min: 3, verdict: servers.Reject}
	l := &socketLease{name: "vm", trial: tr, spec: &servers.Spec{Protocol: "vmess"}}
	ep := seedClassifyEndpoint(b, l)
	src := netip.MustParseAddr("127.0.0.1")
	ep.pending[src] = 1
	c := &blockingBrokerConn{data: []byte("abc"), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { b.classify(ep, &brokerConn{Conn: c, broker: b}, []*socketLease{l}, src); close(done) }()
	<-c.entered
	if c.closed || c.writes != 0 {
		t.Fatalf("drain closed=%t writes=%d", c.closed, c.writes)
	}
	b.mu.Lock()
	pending := len(ep.pending)
	b.mu.Unlock()
	if pending != 0 {
		t.Fatalf("drain occupies classification slot: %v", ep.pending)
	}
	close(c.release)
	<-done
	if !c.closed || c.writes != 0 {
		t.Fatalf("after peer EOF closed=%t writes=%d", c.closed, c.writes)
	}
}

func TestBrokerAmbiguousTrialAcceptsRefused(t *testing.T) {
	b := newSocketBroker()
	b.drainWait = func() time.Duration { return 0 }
	a := &socketLease{name: "a", trial: &brokerTestTrial{min: 3, verdict: servers.Accept, key: []byte("a")}, spec: &servers.Spec{Protocol: "vmess"}}
	z := &socketLease{name: "z", trial: &brokerTestTrial{min: 3, verdict: servers.Accept, key: []byte("z")}, spec: &servers.Spec{Protocol: "vmess"}}
	ep := seedClassifyEndpoint(b, a, z)
	c := &brokerTestConn{input: []byte("abc")}
	src := netip.MustParseAddr("127.0.0.1")
	ep.pending[src] = 1
	b.classify(ep, &brokerConn{Conn: c, broker: b}, []*socketLease{a, z}, src)
	if ep.ambiguous.Load() != 1 || len(a.tcp)+len(z.tcp) != 0 {
		t.Fatalf("ambiguous=%d dispatch=%d", ep.ambiguous.Load(), len(a.tcp)+len(z.tcp))
	}
}
