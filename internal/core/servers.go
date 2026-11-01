package core

import (
	"errors"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
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
	policy     *clientPolicy
	committed  bool
	rolledBack bool
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
func (s *serverSet) prepare(specs []servers.Spec, tunnel C.Tunnel, global []netip.Prefix, accounts map[string][]netip.Prefix) (*serverTransaction, error) {
	s.mu.RLock()
	old := s.items
	s.mu.RUnlock()
	if old == nil {
		old = map[string]*serverEntry{}
	}
	want := make(map[string]servers.Spec, len(specs))
	endpoints := map[string]string{}
	for _, sp := range specs {
		if err := sp.Validate(); err != nil {
			return nil, err
		}
		if _, ok := want[sp.Name]; ok {
			return nil, errors.New("duplicate server name")
		}
		want[sp.Name] = sp.Clone()
		key := sp.ListenAddress()
		if name, ok := endpoints[key]; ok {
			return nil, errors.New("server endpoint conflict: " + name + " and " + sp.Name)
		}
		endpoints[key] = sp.Name
	}
	stage := s.broker.stage()
	next := make(map[string]*serverEntry, len(want))
	policy := newClientPolicy(global, accounts)
	tx := &serverTransaction{set: s, stage: stage, next: next, old: old, policy: policy}
	rollback := func(err error) (*serverTransaction, error) { tx.rollback(); return nil, err }
	for name, sp := range want {
		if e := old[name]; e != nil && e.spec.Equal(sp) {
			next[name] = e
			continue
		}
		p, ok := servers.Lookup(sp.Protocol)
		if !ok {
			return rollback(errors.New("unknown server protocol"))
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
		e.running = servers.Running{Name: sp.Name, Protocol: sp.Protocol, Listen: sp.ListenAddress(), Accounts: append([]string(nil), sp.Accounts...), Closer: r}
		next[name] = e
	}
	return tx, nil
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
	tx.set.mu.Lock()
	tx.set.items = tx.next
	tx.set.mu.Unlock()
	tx.committed = true
	for name, e := range tx.old {
		if tx.next[name] != e {
			_ = e.running.Close()
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
