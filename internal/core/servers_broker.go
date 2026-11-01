package core

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	C "github.com/metacubex/mihomo/constant"
)

type socketBroker struct {
	mu        sync.Mutex
	endpoints map[endpointKey]*socketEndpoint
	conns     map[*brokerConn]struct{}
	pending   int
	closed    bool
	pumps     sync.WaitGroup
	workers   sync.WaitGroup
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
	broker        *socketBroker
	key           endpointKey
	listener      net.Listener
	leases        map[*socketLease]struct{}
	active        *socketLease // legacy unnamed lease
	members       map[string]*socketLease
	changed       chan struct{}
	closed        bool
	unclassified  atomic.Uint64
	slowFirstByte atomic.Uint64
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
}

func newSocketBroker() *socketBroker {
	return &socketBroker{endpoints: make(map[endpointKey]*socketEndpoint), conns: make(map[*brokerConn]struct{})}
}
func (b *socketBroker) stage() *socketStage {
	return &socketStage{broker: b, endpointKeys: make(map[string]struct{})}
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
		if _, exists := s.endpointKeys[stageKey]; exists {
			return nil, errors.New("server endpoint conflict: duplicate endpoint in listener stage")
		}
	} else {
		stageKey += "\x00" + sp.Name
		if _, exists := s.endpointKeys[stageKey]; exists {
			return nil, errors.New("server endpoint conflict: duplicate endpoint in listener stage")
		}
	}
	ep := b.endpoints[key]
	if ep == nil {
		ln, e := net.Listen(n, a)
		if e != nil {
			return nil, e
		}
		ep = &socketEndpoint{broker: b, key: key, listener: ln, leases: map[*socketLease]struct{}{}, members: map[string]*socketLease{}, changed: make(chan struct{})}
		b.endpoints[key] = ep
		b.pumps.Add(1)
		go b.acceptPump(ep)
	}
	if ep.listener == nil {
		return nil, errors.New("listener network conflicts with existing endpoint")
	}
	s.endpointKeys[stageKey] = struct{}{}
	l := &socketLease{endpoint: ep, done: make(chan struct{}), tcp: make(chan net.Conn, 64)}
	if sp != nil {
		v := sp.Clone()
		l.spec = &v
		l.name = sp.Name
	}
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
	s.previous = make(map[*socketEndpoint]endpointSnapshot, len(involved))
	for ep := range involved {
		m := make(map[string]*socketLease, len(ep.members))
		for k, v := range ep.members {
			m[k] = v
		}
		s.previous[ep] = endpointSnapshot{active: ep.active, members: m}
	}
	desiredByName := map[string]*socketLease{}
	for _, sp := range s.desired {
		for _, l := range s.leases {
			if l.name == sp.Name && l.spec != nil && l.spec.Equal(sp) {
				desiredByName[sp.Name] = l
				break
			}
		}
		if desiredByName[sp.Name] == nil {
			for ep := range involved {
				if l := ep.members[sp.Name]; l != nil && l.spec != nil && l.spec.Equal(sp) {
					desiredByName[sp.Name] = l
					break
				}
			}
		}
	}
	for ep := range involved {
		next := map[string]*socketLease{}
		for _, sp := range s.desired {
			if l := desiredByName[sp.Name]; l != nil && l.endpoint == ep {
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
		if b.pending >= 64 {
			b.mu.Unlock()
			_ = owned.Close()
			continue
		}
		b.pending++
		b.workers.Add(1)
		b.mu.Unlock()
		go func(c *brokerConn, initial []*socketLease) {
			defer b.workers.Done()
			defer func() { b.mu.Lock(); b.pending--; b.mu.Unlock() }()
			b.classify(ep, c, initial)
		}(owned, members)
	}
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
func (b *socketBroker) classify(ep *socketEndpoint, c *brokerConn, initial []*socketLease) {
	owned := true
	defer func() {
		if owned {
			_ = c.Close()
		}
	}()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 0, servers.MaxClientHelloBytes)
	tmp := make([]byte, 4096)
	var hello servers.ClientHello
	ok := false
	for len(buf) < servers.MaxClientHelloBytes {
		limit := len(tmp)
		if remaining := servers.MaxClientHelloBytes - len(buf); limit > remaining {
			limit = remaining
		}
		n, e := c.Read(tmp[:limit])
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) >= 3 && !(buf[0] == 22 && buf[1] == 3) {
				break
			}
			h, pe := servers.ParseClientHello(buf)
			if pe == nil {
				hello = h
				ok = true
				break
			}
			if !errors.Is(pe, servers.ErrClientHelloIncomplete) {
				break
			}
		}
		if e != nil {
			var ne net.Error
			if errors.As(e, &ne) && ne.Timeout() {
				ep.slowFirstByte.Add(1)
			}
			break
		}
	}
	if !ok {
		ep.unclassified.Add(1)
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	b.mu.Lock()
	if ep.closed {
		b.mu.Unlock()
		return
	}
	members := activeMembers(ep)
	var chosen *socketLease
	var fallback *socketLease
	matches := 0
	for _, l := range members {
		if l.spec == nil {
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
		b.mu.Unlock()
		ep.unclassified.Add(1)
		return
	}
	if matches == 0 {
		chosen = fallback
	}
	if chosen == nil {
		b.mu.Unlock()
		ep.unclassified.Add(1)
		return
	}
	select {
	case <-chosen.done:
		b.mu.Unlock()
		return
	case chosen.tcp <- &replayConn{Conn: c, prefix: buf}:
		owned = false
		b.mu.Unlock()
	default:
		b.mu.Unlock()
		return
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
				goto drainedDone
			}
		}
	drainedDone:
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

var _ net.Listener = (*socketLease)(nil)
var _ C.InboundListenConfig = (*socketStage)(nil)
