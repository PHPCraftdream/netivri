package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	C "github.com/metacubex/mihomo/constant"
)

type socketBroker struct {
	mu             sync.Mutex
	endpoints      map[endpointKey]*socketEndpoint
	conns          map[*brokerConn]struct{}
	closed         bool
	pumps, workers sync.WaitGroup
	clock          func() time.Time
	drainWait      func() time.Duration
}
type brokerConn struct {
	net.Conn
	broker *socketBroker
	once   sync.Once
}

func (c *brokerConn) Close() error {
	var err error
	c.once.Do(func() { c.broker.mu.Lock(); delete(c.broker.conns, c); c.broker.mu.Unlock(); err = c.Conn.Close() })
	return err
}

type endpointKey struct{ network, address string }
type socketEndpoint struct {
	broker                                           *socketBroker
	key                                              endpointKey
	listener                                         net.Listener
	leases                                           map[*socketLease]struct{}
	active                                           *socketLease
	members                                          map[string]*socketLease
	changed                                          chan struct{}
	closed                                           bool
	unclassified, slowFirstByte, replayed, ambiguous atomic.Uint64
	pending                                          map[netip.Addr]int
	draining                                         int
	replayMu                                         sync.Mutex
	replay                                           map[[32]byte]time.Time
}
type socketStage struct {
	broker              *socketBroker
	mu                  sync.Mutex
	leases              []*socketLease
	finished, activated bool
	previous            map[*socketEndpoint]endpointSnapshot
	endpointKeys        map[string]struct{}
	desired             []servers.Spec
	desiredSet          bool
}
type endpointSnapshot struct {
	active  *socketLease
	members map[string]*socketLease
}
type socketLease struct {
	endpoint *socketEndpoint
	done     chan struct{}
	once     sync.Once
	tcp      chan net.Conn
	spec     *servers.Spec
	name     string
	tls      bool
	trial    servers.Trial
}

func newSocketBroker() *socketBroker {
	return &socketBroker{endpoints: map[endpointKey]*socketEndpoint{}, conns: map[*brokerConn]struct{}{}, clock: time.Now, drainWait: randomDrainWait}
}
func randomDrainWait() time.Duration {
	var b [8]byte
	if _, e := io.ReadFull(rand.Reader, b[:]); e != nil {
		return 60 * time.Second
	}
	return (30 + time.Duration(binary.BigEndian.Uint64(b[:])%61)) * time.Second
}
func (b *socketBroker) stage() *socketStage {
	return &socketStage{broker: b, endpointKeys: map[string]struct{}{}}
}
func (s *socketStage) Listen(ctx context.Context, n, a string) (net.Listener, error) {
	return s.broker.newLease(s, n, a, nil)
}

type serverListenConfig struct {
	stage *socketStage
	spec  servers.Spec
}

func (c serverListenConfig) Listen(ctx context.Context, n, a string) (net.Listener, error) {
	return c.stage.broker.newLease(c.stage, n, a, &c.spec)
}
func (c serverListenConfig) ListenPacket(ctx context.Context, n, a string) (net.PacketConn, error) {
	return c.stage.ListenPacket(ctx, n, a)
}
func (s *socketStage) forServer(sp servers.Spec) C.InboundListenConfig {
	return serverListenConfig{stage: s, spec: sp.Clone()}
}
func (s *socketStage) setServers(specs []servers.Spec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.desiredSet = true
	s.desired = make([]servers.Spec, len(specs))
	for i := range specs {
		s.desired[i] = specs[i].Clone()
	}
}
func (s *socketStage) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return nil, errors.New("UDP listeners are unsupported")
}
func (b *socketBroker) newLease(s *socketStage, n, a string, sp *servers.Spec) (*socketLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil, errors.New("listener stage finished")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, net.ErrClosed
	}
	key := endpointKey{n, a}
	stageKey := n + "\x00" + a
	if sp == nil {
		if _, ok := s.endpointKeys[stageKey]; ok {
			return nil, errors.New("server endpoint conflict: duplicate endpoint in listener stage")
		}
	} else {
		stageKey += "\x00" + sp.Name
		if _, ok := s.endpointKeys[stageKey]; ok {
			return nil, errors.New("server endpoint conflict: duplicate endpoint in listener stage")
		}
	}
	ep := b.endpoints[key]
	if ep == nil {
		ln, e := net.Listen(n, a)
		if e != nil {
			return nil, e
		}
		ep = &socketEndpoint{broker: b, key: key, listener: ln, leases: map[*socketLease]struct{}{}, members: map[string]*socketLease{}, changed: make(chan struct{}), pending: map[netip.Addr]int{}}
		b.endpoints[key] = ep
		b.pumps.Add(1)
		go b.acceptPump(ep)
	}
	if ep.listener == nil {
		return nil, errors.New("listener network conflicts with existing endpoint")
	}
	l := &socketLease{endpoint: ep, done: make(chan struct{}), tcp: make(chan net.Conn, 64)}
	if sp != nil {
		v := sp.Clone()
		l.spec = &v
		l.name = sp.Name
		l.tls, _ = servers.ShareMode(v)
		_, trial := servers.ShareMode(v)
		if trial {
			p, ok := servers.Lookup(v.Protocol)
			auth, supported := p.(servers.TrialAuthenticator)
			if !ok || !supported {
				return nil, errors.New("server trial authenticator unavailable")
			}
			tr, e := auth.NewTrial(v)
			if e != nil {
				return nil, e
			}
			l.trial = tr
		}
	}
	s.endpointKeys[stageKey] = struct{}{}
	ep.leases[l] = struct{}{}
	s.leases = append(s.leases, l)
	return l, nil
}
func signalEndpoint(ep *socketEndpoint)                              { close(ep.changed); ep.changed = make(chan struct{}) }
func containsLease(m map[*socketLease]struct{}, l *socketLease) bool { _, ok := m[l]; return ok }
func (s *socketStage) commit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return errors.New("listener stage finished")
	}
	b := s.broker
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return net.ErrClosed
	}
	for _, l := range s.leases {
		if l.endpoint.closed {
			return net.ErrClosed
		}
		if !containsLease(l.endpoint.leases, l) {
			return errors.New("listener stage lease is closed")
		}
	}
	involved := map[*socketEndpoint]bool{}
	for _, l := range s.leases {
		involved[l.endpoint] = true
	}
	if s.desiredSet {
		for _, ep := range b.endpoints {
			if len(ep.members) > 0 {
				involved[ep] = true
			}
		}
	}
	for _, sp := range s.desired {
		for _, ep := range b.endpoints {
			if ep.key.address == sp.ListenAddress() {
				involved[ep] = true
			}
		}
	}
	s.previous = map[*socketEndpoint]endpointSnapshot{}
	for ep := range involved {
		m := map[string]*socketLease{}
		for k, v := range ep.members {
			m[k] = v
		}
		s.previous[ep] = endpointSnapshot{active: ep.active, members: m}
	}
	desired := map[string]*socketLease{}
	for _, sp := range s.desired {
		for _, l := range s.leases {
			if l.name == sp.Name && l.spec != nil && l.spec.Equal(sp) {
				desired[sp.Name] = l
				break
			}
		}
		if desired[sp.Name] == nil {
			for ep := range involved {
				if l := ep.members[sp.Name]; l != nil && l.spec != nil && l.spec.Equal(sp) {
					desired[sp.Name] = l
					break
				}
			}
		}
	}
	for ep := range involved {
		next := map[string]*socketLease{}
		for _, sp := range s.desired {
			if l := desired[sp.Name]; l != nil && l.endpoint == ep {
				next[sp.Name] = l
			}
		}
		ep.members = next
	}
	for _, l := range s.leases {
		if l.name == "" {
			l.endpoint.active = l
		}
	}
	for ep := range involved {
		signalEndpoint(ep)
	}
	s.activated = true
	s.finished = true
	return nil
}
func (s *socketStage) rollbackActivation() {
	s.mu.Lock()
	if !s.activated {
		s.mu.Unlock()
		return
	}
	b := s.broker
	b.mu.Lock()
	for ep, snap := range s.previous {
		if !ep.closed {
			ep.active = snap.active
			ep.members = snap.members
			signalEndpoint(ep)
		}
	}
	b.mu.Unlock()
	s.activated = false
	ls := append([]*socketLease(nil), s.leases...)
	s.mu.Unlock()
	for _, l := range ls {
		l.close()
	}
}
func (s *socketStage) rollback() {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	s.mu.Unlock()
	for _, l := range s.leases {
		l.close()
	}
}
func (b *socketBroker) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.pumps.Wait()
		b.workers.Wait()
		return
	}
	b.closed = true
	cs := make([]*brokerConn, 0, len(b.conns))
	for c := range b.conns {
		cs = append(cs, c)
	}
	var ls []*socketLease
	for _, ep := range b.endpoints {
		for l := range ep.leases {
			ls = append(ls, l)
		}
		b.closeEndpointLocked(ep)
	}
	b.mu.Unlock()
	for _, l := range ls {
		l.close()
	}
	for _, c := range cs {
		_ = c.Close()
	}
	b.pumps.Wait()
	b.workers.Wait()
}
func (b *socketBroker) closeEndpointLocked(ep *socketEndpoint) {
	if ep.closed {
		return
	}
	ep.closed = true
	signalEndpoint(ep)
	if ep.listener != nil {
		_ = ep.listener.Close()
	}
	delete(b.endpoints, ep.key)
}
func (b *socketBroker) acceptPump(ep *socketEndpoint) {
	defer b.pumps.Done()
	for {
		c, e := ep.listener.Accept()
		if e != nil {
			return
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			_ = c.Close()
			return
		}
		owned := &brokerConn{Conn: c, broker: b}
		b.conns[owned] = struct{}{}
		members := activeMembers(ep)
		if ep.closed || len(members) == 0 {
			b.mu.Unlock()
			_ = owned.Close()
			continue
		}
		if len(members) == 1 {
			l := members[0]
			select {
			case l.tcp <- owned:
				b.mu.Unlock()
			case <-l.done:
				b.mu.Unlock()
				_ = owned.Close()
			default:
				b.mu.Unlock()
				_ = owned.Close()
			}
			continue
		}
		src := sourceAddress(c.RemoteAddr())
		if b.pendingLimit(ep, src) {
			b.mu.Unlock()
			_ = owned.Close()
			continue
		}
		ep.pending[src]++
		b.workers.Add(1)
		b.mu.Unlock()
		go func(c *brokerConn, initial []*socketLease, src netip.Addr) {
			defer b.workers.Done()
			b.classify(ep, c, initial, src)
		}(owned, members, src)
	}
}
func sourceAddress(a net.Addr) netip.Addr {
	if a == nil {
		return netip.Addr{}
	}
	host, _, e := net.SplitHostPort(a.String())
	if e != nil {
		return netip.Addr{}
	}
	ip, e := netip.ParseAddr(host)
	if e != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}
func (b *socketBroker) pendingLimit(ep *socketEndpoint, src netip.Addr) bool {
	total := 0
	for _, n := range ep.pending {
		total += n
	}
	return total >= 64 || ep.pending[src] >= 8
}
func activeMembers(ep *socketEndpoint) []*socketLease {
	out := make([]*socketLease, 0, len(ep.members)+1)
	for _, l := range ep.members {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if ep.active != nil {
		out = append(out, ep.active)
	}
	return out
}
func leaseUsesTLS(l *socketLease) bool {
	if l == nil || l.spec == nil {
		return l != nil && l.tls
	}
	tls, _ := servers.ShareMode(*l.spec)
	return tls
}
func (b *socketBroker) classify(ep *socketEndpoint, c *brokerConn, initial []*socketLease, sources ...netip.Addr) {
	src := netip.Addr{}
	if len(sources) != 0 {
		src = sources[0]
	}
	released := false
	release := func() {
		if released {
			return
		}
		b.mu.Lock()
		if ep.pending != nil {
			ep.pending[src]--
			if ep.pending[src] <= 0 {
				delete(ep.pending, src)
			}
		}
		b.mu.Unlock()
		released = true
	}
	defer release()
	buf := make([]byte, 0, 4<<10)
	limit := 4 << 10
	for _, l := range initial {
		if leaseUsesTLS(l) {
			limit = servers.MaxClientHelloBytes
			break
		}
	}
	if limit > cap(buf) {
		buf = make([]byte, 0, limit)
	}
	deadline := b.clock().Add(3 * time.Second)
	if err := c.SetReadDeadline(deadline); err != nil {
		release()
		ep.unclassified.Add(1)
		b.reject(ep, c)
		return
	}
	trials := make([]*socketLease, 0, len(initial))
	for _, l := range initial {
		if l.trial != nil {
			trials = append(trials, l)
		}
	}
	sort.Slice(trials, func(i, j int) bool {
		mi, mj := trials[i].trial.MinBytes(), trials[j].trial.MinBytes()
		if mi != mj {
			return mi < mj
		}
		return trials[i].name < trials[j].name
	})
	tried := map[*socketLease]bool{}
	var accepted []*socketLease
	trialKeys := make(map[*socketLease][]byte)
	var hello servers.ClientHello
	tlsValid := false
	tlsBad := false
	readFailed := false
	tmp := make([]byte, 4096)
	for len(buf) < limit {
		nmax := len(tmp)
		if rem := limit - len(buf); nmax > rem {
			nmax = rem
		}
		n, e := c.Read(tmp[:nmax])
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for _, l := range trials {
				if l.trial == nil || tried[l] || len(buf) < l.trial.MinBytes() {
					continue
				}
				tried[l] = true
				v, key := l.trial.Try(buf[:l.trial.MinBytes()], b.clock())
				if v == servers.Accept {
					accepted = append(accepted, l)
					trialKeys[l] = append([]byte(nil), key...)
				} else if v == servers.NeedMore {
					tried[l] = false
				}
			}
			if len(buf) >= 2 && buf[0] == 0x16 && buf[1] == 3 && !tlsValid && !tlsBad {
				h, pe := servers.ParseClientHello(buf)
				if pe == nil {
					hello = h
					tlsValid = true
				} else if !errors.Is(pe, servers.ErrClientHelloIncomplete) {
					tlsBad = true
				}
			}
			allTrialsDone := len(tried) == len(trials)
			if len(accepted) > 0 || (tlsValid && allTrialsDone) || ((tlsBad || len(buf) >= 2 && !(buf[0] == 0x16 && buf[1] == 3)) && allTrialsDone) {
				break
			}
		}
		if e != nil {
			if n == 0 {
				readFailed = true
			}
			var ne net.Error
			if errors.As(e, &ne) && ne.Timeout() {
				ep.slowFirstByte.Add(1)
			}
			break
		}
	}
	release()
	b.mu.Lock()
	if ep.closed {
		b.mu.Unlock()
		b.reject(ep, c)
		return
	}
	live := func(l *socketLease) bool {
		return !l.endpoint.closed && containsLease(l.endpoint.leases, l) && (l.endpoint.members[l.name] == l || l.endpoint.active == l)
	}
	var chosen *socketLease
	good := make([]*socketLease, 0, len(accepted))
	for _, l := range accepted {
		if live(l) {
			good = append(good, l)
		}
	}
	if len(good) > 1 {
		ep.ambiguous.Add(1)
		ep.unclassified.Add(1)
		b.mu.Unlock()
		b.reject(ep, c)
		return
	}
	if len(good) == 1 {
		chosen = good[0]
	}
	if chosen == nil && tlsValid {
		matches := 0
		var fallback *socketLease
		for _, l := range activeMembers(ep) {
			if l.spec == nil || !leaseUsesTLS(l) {
				continue
			}
			if l.spec.Default && fallback == nil {
				fallback = l
			}
			if servers.MatchClientHello(*l.spec, hello) {
				chosen = l
				matches++
			}
		}
		if matches > 1 {
			ep.ambiguous.Add(1)
			ep.unclassified.Add(1)
			b.mu.Unlock()
			b.reject(ep, c)
			return
		}
		if matches == 0 {
			chosen = fallback
		}
	}
	if chosen != nil && len(good) == 0 && len(trials) > 0 {
		allRejected := true
		for _, l := range trials {
			if !tried[l] {
				allRejected = false
				break
			}
		}
		if !allRejected {
			chosen = nil
		}
	}
	if chosen == nil || readFailed {
		b.mu.Unlock()
		ep.unclassified.Add(1)
		b.reject(ep, c)
		return
	}
	if len(good) == 1 && good[0].trial != nil {
		if b.replayCheckAndRecord(ep, trialKeys[good[0]], good[0].spec.Protocol) {
			ep.replayed.Add(1)
			b.mu.Unlock()
			b.reject(ep, c)
			return
		}
	}
	select {
	case <-chosen.done:
		b.mu.Unlock()
		b.reject(ep, c)
		return
	case chosen.tcp <- &replayConn{Conn: c, prefix: buf}:
		_ = c.SetReadDeadline(time.Time{})
		b.mu.Unlock()
		return
	default:
		b.mu.Unlock()
		b.reject(ep, c)
		return
	}
}
func (b *socketBroker) replayCheckAndRecord(ep *socketEndpoint, key []byte, protocol string) bool {
	h := sha256.Sum256(append([]byte(protocol+"\x00"), key...))
	now := b.clock()
	ep.replayMu.Lock()
	defer ep.replayMu.Unlock()
	if ep.replay == nil {
		ep.replay = map[[32]byte]time.Time{}
	}
	for k, x := range ep.replay {
		if !x.After(now) {
			delete(ep.replay, k)
		}
	}
	if _, ok := ep.replay[h]; ok {
		return true
	}
	if len(ep.replay) >= 4096 {
		return true
	}
	// A prefix stamped one window ahead of the first acceptance stays valid
	// for two windows (28 s for Shadowsocks 2022, 118 s for VMess): the entry
	// must outlive that.
	ttl := 240 * time.Second
	if protocol == "shadowsocks" {
		ttl = 60 * time.Second
	}
	ep.replay[h] = now.Add(ttl)
	return false
}
func (b *socketBroker) reject(ep *socketEndpoint, c *brokerConn) {
	b.mu.Lock()
	if b.closed || ep.draining >= 32 {
		b.mu.Unlock()
		_ = c.Close()
		return
	}
	ep.draining++
	b.mu.Unlock()
	defer func() { b.mu.Lock(); ep.draining--; b.mu.Unlock(); _ = c.Close() }()
	idle := b.drainWait()
	if err := c.SetDeadline(b.clock().Add(idle)); err != nil {
		return
	}
	buf := make([]byte, 1024)
	total := 0
	for total < 64<<10 {
		n, e := c.Read(buf)
		total += n
		if e != nil || n == 0 {
			return
		}
		if err := c.SetDeadline(b.clock().Add(idle)); err != nil {
			return
		}
	}
}

type replayConn struct {
	net.Conn
	prefix []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
func (l *socketLease) close() {
	l.once.Do(func() {
		close(l.done)
		ep := l.endpoint
		b := ep.broker
		b.mu.Lock()
		delete(ep.leases, l)
		if ep.active == l {
			ep.active = nil
		}
		for n, v := range ep.members {
			if v == l {
				delete(ep.members, n)
			}
		}
		signalEndpoint(ep)
		var drained []net.Conn
		for {
			select {
			case c := <-l.tcp:
				if c != nil {
					drained = append(drained, c)
				}
			default:
				goto done
			}
		}
	done:
		if len(ep.leases) == 0 {
			b.closeEndpointLocked(ep)
		}
		b.mu.Unlock()
		for _, c := range drained {
			_ = c.Close()
		}
	})
}
func (l *socketLease) Accept() (net.Conn, error) {
	for {
		l.endpoint.broker.mu.Lock()
		active := l.endpoint.active == l || l.name != "" && l.endpoint.members[l.name] == l
		changed := l.endpoint.changed
		l.endpoint.broker.mu.Unlock()
		if !active {
			select {
			case <-l.done:
				return nil, net.ErrClosed
			case <-changed:
			}
			continue
		}
		select {
		case <-l.done:
			return nil, net.ErrClosed
		case c := <-l.tcp:
			if c != nil {
				l.endpoint.broker.mu.Lock()
				active := l.endpoint.active == l || l.name != "" && l.endpoint.members[l.name] == l
				l.endpoint.broker.mu.Unlock()
				if !active {
					_ = c.Close()
					continue
				}
				return c, nil
			}
		case <-changed:
		}
	}
}
func (l *socketLease) Addr() net.Addr { return l.endpoint.listener.Addr() }
func (l *socketLease) Close() error   { l.close(); return nil }
func (b *socketBroker) endpointInfo(address string) (members []string, unclassified, slow uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ep := range b.endpoints {
		if ep.key.address == address {
			for n := range ep.members {
				members = append(members, n)
			}
			if ep.active != nil {
				members = append(members, ep.active.name)
			}
			sort.Strings(members)
			return members, ep.unclassified.Load(), ep.slowFirstByte.Load()
		}
	}
	return nil, 0, 0
}
func (b *socketBroker) endpointCounters(address string) (replayed, ambiguous uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ep := range b.endpoints {
		if ep.key.address == address {
			return ep.replayed.Load(), ep.ambiguous.Load()
		}
	}
	return 0, 0
}

var _ net.Listener = (*socketLease)(nil)
var _ C.InboundListenConfig = (*socketStage)(nil)
