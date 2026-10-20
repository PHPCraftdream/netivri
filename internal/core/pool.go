package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/dialer"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
)

type idleTCP struct {
	conn net.Conn
	at   time.Time
}
type poolDialer struct {
	mu         sync.Mutex
	addr       string
	size       int
	ttl        time.Duration
	underlying C.Dialer
	idle       []idleTCP
	closed     bool
	wake       chan struct{}
	done       chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	owner      *warmer
	startOnce  sync.Once
	stopOnce   sync.Once
}

func newPoolDialer(node map[string]any, size int, ttl time.Duration, owner *warmer) (*poolDialer, error) {
	mappings, _, err := prepareNode(node)
	if err != nil {
		return nil, err
	}
	m := mappings[0]
	typ, _ := m["type"].(string)
	supported := map[string]bool{"ss": true, "ssr": true, "socks5": true, "http": true, "vmess": true, "vless": true, "trojan": true}
	if !supported[typ] {
		return nil, fmt.Errorf("core: TCP pool unsupported for proxy type %q", typ)
	}
	if raw, exists := m["network"]; exists {
		network, ok := raw.(string)
		if !ok {
			return nil, errors.New("core: unsupported network representation for TCP pool")
		}
		if network != "" && network != "tcp" {
			return nil, errors.New("core: TCP pool requires TCP network")
		}
	}
	for _, key := range []string{"smux", "plugin", "plugin-opts", "dialer-proxy", "udp", "snell-version", "reuse"} {
		if _, exists := m[key]; exists {
			return nil, fmt.Errorf("core: TCP pool unsupported with %s", key)
		}
	}
	server, _ := m["server"].(string)
	port, err := numericPort(m["port"])
	if err != nil {
		return nil, err
	}
	var opts []dialer.Option
	if raw, exists := m["interface-name"]; exists {
		iface, ok := raw.(string)
		if !ok {
			return nil, errors.New("core: unsupported interface-name representation for TCP pool")
		}
		if iface != "" {
			opts = append(opts, dialer.WithInterface(iface))
		}
	}
	if value, ok := m["routing-mark"]; ok {
		routingMark, ok := value.(int)
		if !ok {
			return nil, errors.New("core: unsupported routing-mark representation for TCP pool")
		}
		if routingMark != 0 {
			opts = append(opts, dialer.WithRoutingMark(routingMark))
		}
	}
	if raw, exists := m["tfo"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return nil, errors.New("core: unsupported tfo representation for TCP pool")
		}
		if value {
			opts = append(opts, dialer.WithTFO(true))
		}
	}
	if raw, exists := m["mptcp"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return nil, errors.New("core: unsupported mptcp representation for TCP pool")
		}
		if value {
			opts = append(opts, dialer.WithMPTCP(true))
		}
	}
	if value, ok := m["ip-version"]; ok {
		version, ok := value.(string)
		if !ok {
			return nil, errors.New("core: unsupported ip-version representation for TCP pool")
		}
		switch version {
		case "dual":
		case "ipv4":
			opts = append(opts, dialer.WithOnlySingleStack(true))
		case "ipv6":
			opts = append(opts, dialer.WithOnlySingleStack(false))
		case "ipv4-prefer":
			opts = append(opts, dialer.WithPreferIPv4())
		case "ipv6-prefer":
			opts = append(opts, dialer.WithPreferIPv6())
		default:
			return nil, errors.New("core: unsupported ip-version for TCP pool")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &poolDialer{
		addr:       net.JoinHostPort(server, strconv.Itoa(port)),
		size:       size,
		ttl:        ttl,
		underlying: dialer.NewDialer(opts...),
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		owner:      owner,
	}, nil
}

func numericPort(value any) (int, error) {
	var port int
	switch v := value.(type) {
	case int:
		port = v
	case float64:
		port = int(v)
		if float64(port) != v {
			return 0, errors.New("core: invalid TCP pool port")
		}
	case string:
		var err error
		port, err = strconv.Atoi(v)
		if err != nil {
			return 0, errors.New("core: invalid TCP pool port")
		}
	default:
		return 0, errors.New("core: invalid TCP pool port")
	}
	if port < 1 || port > 65535 {
		return 0, errors.New("core: invalid TCP pool port")
	}
	return port, nil
}

func (p *poolDialer) start() {
	p.startOnce.Do(func() {
		p.mu.Lock()
		stopped := p.closed
		p.mu.Unlock()
		if stopped {
			close(p.done)
			return
		}
		go p.refill()
	})
}

// DialContext serves the pooled address from the idle sockets. The mutex
// is never held across the 1 ms socket-liveness read: a candidate is
// popped under the lock and checked outside, so a slow check does not
// block other dialers.
func (p *poolDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || address != p.addr {
		return p.underlying.DialContext(ctx, network, address)
	}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, net.ErrClosed
		}
		if len(p.idle) == 0 {
			p.mu.Unlock()
			p.owner.addPoolMetric("miss")
			p.signal()
			return p.underlying.DialContext(ctx, network, address)
		}
		last := len(p.idle) - 1
		entry := p.idle[last]
		p.idle = p.idle[:last]
		p.mu.Unlock()

		if p.ttl > 0 && time.Since(entry.at) >= p.ttl || !poolSocketAlive(entry.conn) {
			_ = entry.conn.Close()
			p.owner.addPoolMetric("discard")
			continue
		}
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			_ = entry.conn.Close()
			return nil, net.ErrClosed
		}
		p.owner.addPoolMetric("hit")
		p.signal()
		return entry.conn, nil
	}
}

// poolSocketAlive uses a short nonblocking read; EOF, unexpected data, and
// non-timeout errors cause the idle socket to be discarded.
func poolSocketAlive(conn net.Conn) bool {
	if err := conn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		return false
	}
	var b [1]byte
	n, err := conn.Read(b[:])
	if resetErr := conn.SetReadDeadline(time.Time{}); resetErr != nil {
		return false
	}
	if n != 0 || err == nil {
		return false
	}
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func (p *poolDialer) ListenPacket(ctx context.Context, network, address string, remote netip.AddrPort) (net.PacketConn, error) {
	return p.underlying.ListenPacket(ctx, network, address, remote)
}
func (p *poolDialer) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *poolDialer) refill() {
	defer close(p.done)
	for {
		p.mu.Lock()
		need := !p.closed && len(p.idle) < p.size
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return
		}
		if !need {
			select {
			case <-p.ctx.Done():
				return
			case <-p.wake:
				continue
			case <-time.After(time.Second):
				p.expire()
				continue
			}
		}
		ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
		conn, err := p.underlying.DialContext(ctx, "tcp", p.addr)
		cancel()
		if err != nil {
			select {
			case <-p.ctx.Done():
				return
			case <-p.wake:
			case <-time.After(time.Second):
			}
			continue
		}
		p.mu.Lock()
		closed = p.closed
		parked := false
		if !closed && len(p.idle) < p.size {
			p.idle = append(p.idle, idleTCP{conn: conn, at: time.Now()})
			parked = true
		}
		p.mu.Unlock()
		if !parked {
			_ = conn.Close()
		}
		if closed {
			return
		}
	}
}

// expire drops dead or expired idle sockets. The liveness checks run
// without the mutex: the slice is taken under the lock, checked outside
// and the kept entries are merged back (entries added meanwhile stay,
// the size cap holds, and a pool closed meanwhile has its kept
// connections closed).
func (p *poolDialer) expire() {
	p.mu.Lock()
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()

	now := time.Now()
	var kept []idleTCP
	for _, entry := range idle {
		if p.ttl > 0 && now.Sub(entry.at) >= p.ttl || !poolSocketAlive(entry.conn) {
			_ = entry.conn.Close()
			p.owner.addPoolMetric("discard")
			continue
		}
		kept = append(kept, entry)
	}

	// Merge back under the lock; closed is read here, not before the checks,
	// so a stop() that ran meanwhile is seen.
	p.mu.Lock()
	var drop []idleTCP
	if p.closed {
		drop = kept
		kept = nil
	} else {
		kept = append(kept, p.idle...)
		if extra := len(kept) - p.size; extra > 0 {
			// Oldest first: they are the closest to their TTL.
			drop = kept[:extra]
			kept = kept[extra:]
		}
	}
	p.idle = kept
	p.mu.Unlock()
	for _, entry := range drop {
		_ = entry.conn.Close()
		p.owner.addPoolMetric("discard")
	}
}

func (p *poolDialer) stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cancel()
		for _, entry := range p.idle {
			_ = entry.conn.Close()
		}
		p.idle = nil
		p.mu.Unlock()
		p.startOnce.Do(func() { close(p.done) })
	})
}
func (p *poolDialer) wait(timeout time.Duration) {
	select {
	case <-p.done:
	case <-time.After(timeout):
	}
}
func (p *poolDialer) close() {
	p.stop()
	p.wait(5 * time.Second)
}

func (w *warmer) installPool(node map[string]any) error {
	mappings, _, err := prepareNode(node)
	if err != nil {
		return err
	}
	proxy, err := adapter.ParseProxy(mappings[0], adapter.WithDialerForAPI(w.pool))
	if err != nil {
		return errors.New("core: failed to parse pooled proxy")
	}
	// The MATCH rule resolves netivri-node by name. Replace only that entry;
	// retain all groups and pass the existing provider map unchanged.
	proxies := tunnel.Proxies()
	updated := make(map[string]C.Proxy, len(proxies)+1)
	for name, existing := range proxies {
		updated[name] = existing
	}
	updated[nodeName] = proxy
	tunnel.UpdateProxies(updated, tunnel.Providers())
	return nil
}

var _ C.Dialer = (*poolDialer)(nil)
