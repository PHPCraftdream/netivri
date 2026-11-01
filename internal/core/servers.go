package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
	inbound "github.com/metacubex/mihomo/adapter/inbound"
	C "github.com/metacubex/mihomo/constant"
)

type serverSet struct {
	mu     sync.RWMutex
	items  map[string]*serverEntry
	broker *socketBroker
	policy atomic.Pointer[clientPolicy]
}
type clientPolicy struct {
	global   []netip.Prefix
	accounts map[string][]netip.Prefix
}
type serverCounters struct {
	connections atomic.Int64
	dropped     atomic.Uint64
}
type serverEntry struct {
	running  servers.Running
	spec     servers.Spec
	counters *serverCounters
}
type serverTunnel struct {
	inner  C.Tunnel
	entry  *serverEntry
	policy *atomic.Pointer[clientPolicy]
}
type serverTransaction struct {
	set        *serverSet
	stage      *socketStage
	next       map[string]*serverEntry
	old        map[string]*serverEntry
	udp        map[string]servers.Protocol
	specs      map[string]servers.Spec
	tunnel     C.Tunnel
	policy     *clientPolicy
	committed  bool
	rolledBack bool
}
type serverUDPListenConfig struct{ listen C.InboundListenConfig }

func (c serverUDPListenConfig) Listen(context.Context, string, string) (net.Listener, error) {
	return nil, errors.New("TCP listening is unsupported for UDP-bound server")
}
func (c serverUDPListenConfig) ListenPacket(ctx context.Context, n, a string) (net.PacketConn, error) {
	switch n {
	case "udp", "udp4", "udp6":
	default:
		return nil, errors.New("UDP-bound server requires UDP network")
	}
	return c.listen.ListenPacket(ctx, n, a)
}
func (t *serverTunnel) HandleTCPConn(c net.Conn, m *C.Metadata) {
	p := t.policy.Load()
	if !permitsServerClient(p, m) {
		t.entry.counters.dropped.Add(1)
		_ = c.Close()
		return
	}
	t.entry.counters.connections.Add(1)
	defer t.entry.counters.connections.Add(-1)
	t.inner.HandleTCPConn(c, m)
}
func (t *serverTunnel) HandleUDPPacket(p C.UDPPacket, m *C.Metadata) {
	t.entry.counters.dropped.Add(1)
	p.Drop()
}
func (t *serverTunnel) NatTable() C.NatTable { return t.inner.NatTable() }
func permitsServerClient(p *clientPolicy, m *C.Metadata) bool {
	if m == nil {
		return false
	}
	if p == nil {
		return true
	}
	ip := m.SrcIP.Unmap()
	if !prefixAllows(p.global, ip) {
		return false
	}
	if a, ok := p.accounts[m.InUser]; ok {
		return prefixAllows(a, ip)
	}
	return true
}
func prefixAllows(ps []netip.Prefix, ip netip.Addr) bool {
	if ps == nil {
		return true
	}
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
func clonePrefixes(in []netip.Prefix) []netip.Prefix {
	if in == nil {
		return nil
	}
	return append(make([]netip.Prefix, 0, len(in)), in...)
}
func newClientPolicy(global []netip.Prefix, accounts map[string][]netip.Prefix) *clientPolicy {
	out := make(map[string][]netip.Prefix, len(accounts))
	for k, v := range accounts {
		out[k] = clonePrefixes(v)
	}
	return &clientPolicy{global: clonePrefixes(global), accounts: out}
}
func protocolUDPBound(p servers.Protocol) bool {
	c, ok := p.(servers.UDPBound)
	return ok && c.UDPBound()
}
func (s *serverSet) prepare(specs []servers.Spec, tunnel C.Tunnel, global []netip.Prefix, accounts map[string][]netip.Prefix) (*serverTransaction, error) {
	s.mu.RLock()
	old := s.items
	s.mu.RUnlock()
	if old == nil {
		old = map[string]*serverEntry{}
	}
	want := make(map[string]servers.Spec, len(specs))
	endpoints := map[string]string{}
	udp := map[string]servers.Protocol{}
	for _, sp := range specs {
		if err := sp.Validate(); err != nil {
			return nil, err
		}
		if _, ok := want[sp.Name]; ok {
			return nil, errors.New("duplicate server name")
		}
		p, ok := servers.Lookup(sp.Protocol)
		if !ok {
			return nil, errors.New("unknown server protocol")
		}
		transport := "tcp"
		if protocolUDPBound(p) {
			transport = "udp"
			udp[sp.Name] = p
		}
		want[sp.Name] = sp.Clone()
		key := transport + "\x00" + sp.ListenAddress()
		if name, ok := endpoints[key]; ok {
			return nil, errors.New("server endpoint conflict: " + name + " and " + sp.Name)
		}
		endpoints[key] = sp.Name
	}
	stage := s.broker.stage()
	next := make(map[string]*serverEntry, len(want))
	policy := newClientPolicy(global, accounts)
	tx := &serverTransaction{set: s, stage: stage, next: next, old: old, udp: udp, specs: want, tunnel: tunnel, policy: policy}
	rollback := func(err error) (*serverTransaction, error) { tx.rollback(); return nil, err }
	for name, sp := range want {
		if e := old[name]; e != nil && e.spec.Equal(sp) {
			next[name] = e
			continue
		}
		p, _ := servers.Lookup(sp.Protocol)
		if protocolUDPBound(p) {
			continue
		}
		e := &serverEntry{spec: sp.Clone(), counters: &serverCounters{}}
		if previous := old[name]; previous != nil {
			e.counters = previous.counters
		}
		tw := &serverTunnel{inner: tunnel, entry: e, policy: &s.policy}
		r, err := p.Start(sp, servers.Env{Tunnel: tw, ListenConfig: stage})
		if err != nil {
			if r != nil {
				_ = r.Close()
			}
			if isAddrInUse(err) {
				return rollback(&cli.Error{Code: "PORT_IN_USE", Message: "server listen port is already in use", Exit: cli.ExitConflict})
			}
			return rollback(err)
		}
		e.running = serverRunning(sp, r)
		next[name] = e
	}
	return tx, nil
}
func serverRunning(sp servers.Spec, c interface{ Close() error }) servers.Running {
	return servers.Running{Name: sp.Name, Protocol: sp.Protocol, Listen: sp.ListenAddress(), Accounts: append([]string(nil), sp.Accounts...), Closer: c}
}
func (tx *serverTransaction) startUDP(name string, sp servers.Spec, p servers.Protocol) (*serverEntry, error) {
	e := &serverEntry{spec: sp.Clone(), counters: &serverCounters{}}
	if old := tx.old[name]; old != nil {
		e.counters = old.counters
	}
	tw := &serverTunnel{inner: tx.tunnel, entry: e, policy: &tx.set.policy}
	r, err := p.Start(sp, servers.Env{Tunnel: tw, ListenConfig: serverUDPListenConfig{listen: inbound.NewListenConfig()}})
	if err != nil {
		if r != nil {
			_ = r.Close()
		}
		return nil, err
	}
	e.running = serverRunning(sp, r)
	return e, nil
}
func (tx *serverTransaction) rollback() {
	if tx == nil || tx.committed || tx.rolledBack {
		return
	}
	tx.rolledBack = true
	for name, e := range tx.next {
		if tx.old[name] != e {
			_ = e.running.Close()
		}
	}
	tx.stage.rollback()
}

// commit activates prepared listeners. Callers hold lifecycleMu, so broker
// closure cannot race this preflight/activation; policy is published first so
// no newly accepted server connection observes an unfiltered nil policy.
func isUDPEntry(e *serverEntry) bool {
	if e == nil {
		return false
	}
	p, ok := servers.Lookup(e.spec.Protocol)
	return ok && protocolUDPBound(p)
}
func (tx *serverTransaction) commit() error {
	tx.set.mu.Lock()
	previousPolicy := tx.set.policy.Load()
	tx.set.policy.Store(tx.policy)
	tx.set.mu.Unlock()
	if err := tx.stage.commit(); err != nil {
		tx.set.mu.Lock()
		tx.set.policy.Store(previousPolicy)
		tx.set.mu.Unlock()
		return err
	}
	changed := make([]string, 0)
	for name, e := range tx.old {
		if tx.next[name] != e && isUDPEntry(e) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	for _, n := range changed {
		_ = tx.old[n].running.Close()
	}
	names := make([]string, 0)
	for n := range tx.udp {
		if e := tx.old[n]; e == nil || !e.spec.Equal(tx.specs[n]) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var failure error
	for _, n := range names {
		e, err := tx.startUDP(n, tx.specs[n], tx.udp[n])
		if err != nil {
			failure = err
			break
		}
		tx.next[n] = e
	}
	if failure != nil {
		tx.stage.rollbackActivation()
		tx.stage.rollback()
		for n, e := range tx.next {
			if tx.old[n] != e {
				_ = e.running.Close()
			}
		}
		tx.set.mu.Lock()
		tx.set.policy.Store(previousPolicy)
		tx.set.mu.Unlock()
		restoreFailed := false
		for _, n := range changed {
			old := tx.old[n]
			p, ok := servers.Lookup(old.spec.Protocol)
			if !ok {
				restoreFailed = true
				continue
			}
			r, err := tx.startUDP(n, old.spec, p)
			if err != nil {
				restoreFailed = true
				continue
			}
			tx.set.mu.Lock()
			old.running = r.running
			tx.set.mu.Unlock()
		}
		tx.set.mu.Lock()
		tx.set.items = tx.old
		tx.set.mu.Unlock()
		tx.rolledBack = true
		var ce *cli.Error
		if isAddrInUse(failure) && !errors.As(failure, &ce) {
			failure = &cli.Error{Code: "PORT_IN_USE", Message: "server listen port is already in use", Exit: cli.ExitConflict}
		}
		if restoreFailed {
			return fmt.Errorf("%w (previous servers not fully restored)", failure)
		}
		return fmt.Errorf("%w (previous servers restored)", failure)
	}
	tx.set.mu.Lock()
	tx.set.items = tx.next
	tx.set.mu.Unlock()
	tx.committed = true
	for n, old := range tx.old {
		if tx.next[n] != old && !isUDPEntry(old) {
			_ = old.running.Close()
		}
	}
	return nil
}
func (s *serverSet) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, e := range s.items {
		_ = e.running.Close()
	}
	s.items = nil
	s.mu.Unlock()
	s.broker.close()
}
func (s *serverSet) info() []servers.Info {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]servers.Info, 0, len(s.items))
	for _, e := range s.items {
		out = append(out, servers.Info{Name: e.running.Name, Protocol: e.running.Protocol, Listen: e.running.Listen, Accounts: append([]string(nil), e.running.Accounts...), Connections: int(e.counters.connections.Load()), DroppedClients: e.counters.dropped.Load()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
