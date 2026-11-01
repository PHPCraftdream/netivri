package core

import (
	"context"
	"errors"
	"net"
	"sync"

	C "github.com/metacubex/mihomo/constant"
)

type socketBroker struct {
	mu        sync.Mutex
	endpoints map[endpointKey]*socketEndpoint
	conns     map[*brokerConn]struct{}
	closed    bool
	pumps     sync.WaitGroup
}
type brokerConn struct {
	net.Conn
	broker *socketBroker
	once   sync.Once
}

func (c *brokerConn) Close() error {
	var err error
	c.once.Do(func() {
		c.broker.mu.Lock()
		delete(c.broker.conns, c)
		c.broker.mu.Unlock()
		err = c.Conn.Close()
	})
	return err
}

type endpointKey struct{ network, address string }
type socketEndpoint struct {
	broker   *socketBroker
	key      endpointKey
	listener net.Listener
	leases   map[*socketLease]struct{}
	active   *socketLease
	changed  chan struct{}
	closed   bool
}
type socketStage struct {
	broker       *socketBroker
	mu           sync.Mutex
	leases       []*socketLease
	finished     bool
	endpointKeys map[string]struct{}
}
type socketLease struct {
	endpoint *socketEndpoint
	done     chan struct{}
	once     sync.Once
	tcp      chan net.Conn
}

func newSocketBroker() *socketBroker {
	return &socketBroker{endpoints: make(map[endpointKey]*socketEndpoint), conns: make(map[*brokerConn]struct{})}
}
func (b *socketBroker) stage() *socketStage {
	return &socketStage{broker: b, endpointKeys: make(map[string]struct{})}
}
func (s *socketStage) Listen(ctx context.Context, n, a string) (net.Listener, error) {
	return s.broker.newLease(s, n, a)
}
func (s *socketStage) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return nil, errors.New("UDP listeners are unsupported")
}
func (b *socketBroker) newLease(s *socketStage, n, a string) (*socketLease, error) {
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
	if _, exists := s.endpointKeys[stageKey]; exists {
		return nil, errors.New("server endpoint conflict: duplicate endpoint in listener stage")
	}
	ep := b.endpoints[key]
	if ep == nil {
		listener, err := net.Listen(n, a)
		if err != nil {
			return nil, err
		}
		ep = &socketEndpoint{broker: b, key: key, listener: listener, leases: make(map[*socketLease]struct{}), changed: make(chan struct{})}
		b.endpoints[key] = ep
		b.pumps.Add(1)
		go b.acceptPump(ep)
	}
	if ep.listener == nil {
		return nil, errors.New("listener network conflicts with existing endpoint")
	}
	s.endpointKeys[stageKey] = struct{}{}
	l := &socketLease{endpoint: ep, done: make(chan struct{}), tcp: make(chan net.Conn, 64)}
	ep.leases[l] = struct{}{}
	s.leases = append(s.leases, l)
	return l, nil
}
func signalEndpoint(ep *socketEndpoint) { close(ep.changed); ep.changed = make(chan struct{}) }
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
	}
	for _, l := range s.leases {
		l.endpoint.active = l
		signalEndpoint(l.endpoint)
	}
	s.finished = true
	return nil
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
		return
	}
	b.closed = true
	conns := make([]*brokerConn, 0, len(b.conns))
	for c := range b.conns {
		conns = append(conns, c)
	}
	var leases []*socketLease
	for _, ep := range b.endpoints {
		for l := range ep.leases {
			leases = append(leases, l)
		}
		b.closeEndpointLocked(ep)
	}
	b.mu.Unlock()
	for _, l := range leases {
		l.close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	b.pumps.Wait()
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
		c, err := ep.listener.Accept()
		if err != nil {
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
		c = owned
		l := ep.active
		if ep.closed || l == nil {
			b.mu.Unlock()
			_ = c.Close()
			continue
		}
		for ep.active == l && !ep.closed {
			select {
			case l.tcp <- c:
				b.mu.Unlock()
				c = nil
			case <-l.done:
				b.mu.Unlock()
				_ = c.Close()
				c = nil
			case <-ep.changed:
				continue
			default:
				b.mu.Unlock()
				_ = c.Close()
				c = nil
			}
			break
		}
		if c != nil {
			b.mu.Unlock()
			_ = c.Close()
		}
	}
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
			signalEndpoint(ep)
		}
		var drained []net.Conn
		for {
			select {
			case c := <-l.tcp:
				if c != nil {
					drained = append(drained, c)
				}
			default:
				goto drainedQueue
			}
		}
	drainedQueue:
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
		active := l.endpoint.active == l
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
				select {
				case <-l.done:
					_ = c.Close()
					return nil, net.ErrClosed
				default:
					return c, nil
				}
			}
		case <-changed:
		}
	}
}
func (l *socketLease) Addr() net.Addr { return l.endpoint.listener.Addr() }
func (l *socketLease) Close() error   { l.close(); return nil }

var _ net.Listener = (*socketLease)(nil)
var _ C.InboundListenConfig = (*socketStage)(nil)
