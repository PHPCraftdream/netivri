package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

const serverDiagnosticsEvery = time.Minute

func resolveServerSpecs(home string, cfg *config.Config) ([]servers.Spec, error) {
	specs, err := cfg.ResolveServers()
	if err != nil {
		return nil, err
	}
	store := servers.NewStore(home)
	for i := range specs {
		specs[i], err = store.EnsureSecrets(specs[i])
		if err != nil {
			return nil, fmt.Errorf("server %s: %w", specs[i].Name, err)
		}
	}
	return specs, nil
}

type serverInfoSource interface{ Servers() []servers.Info }

func cloneServerSpecs(in []servers.Spec) []servers.Spec {
	if in == nil {
		return nil
	}
	out := make([]servers.Spec, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

func accountAllowedClients(cfg *config.Config) map[string][]netip.Prefix {
	out := make(map[string][]netip.Prefix, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		out[a.Login] = config.AllowedClientPrefixes(a.AllowedClients)
	}
	return out
}

func serverFingerprint(specs []servers.Spec) string {
	ordered := cloneServerSpecs(specs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	data, _ := json.Marshal(ordered)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (l *liveAngel) logServerDelta(old, next []servers.Spec) {
	before, after := map[string]servers.Spec{}, map[string]servers.Spec{}
	for _, s := range old {
		before[s.Name] = s
	}
	for _, s := range next {
		after[s.Name] = s
	}
	names := make([]string, 0, len(before)+len(after))
	seen := map[string]bool{}
	for name := range before {
		names = append(names, name)
		seen[name] = true
	}
	for name := range after {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		a, aok := before[name]
		b, bok := after[name]
		switch {
		case !aok && bok:
			l.logf("server %s (%s) listening on %s for accounts %s", b.Name, b.Protocol, b.ListenAddress(), strings.Join(b.Accounts, ","))
		case aok && !bok:
			l.logf("server %s stopped", a.Name)
		case aok && bok && !a.Equal(b):
			l.logf("server %s stopped", a.Name)
			l.logf("server %s (%s) listening on %s for accounts %s", b.Name, b.Protocol, b.ListenAddress(), strings.Join(b.Accounts, ","))
		}
	}
}

func (l *liveAngel) flushServerDiagnostics(source serverInfoSource) {
	if source == nil {
		return
	}
	infos := source.Servers()
	l.serverLogMu.Lock()
	defer l.serverLogMu.Unlock()
	if l.serverPrevDrops == nil {
		l.serverPrevDrops = map[string]uint64{}
	}
	present := make(map[string]bool)
	for _, info := range infos {
		present[info.Name] = true
		prev := l.serverPrevDrops[info.Name]
		delta := info.DroppedClients
		if delta >= prev {
			delta -= prev
		}
		l.serverPrevDrops[info.Name] = info.DroppedClients
		if delta >= 100 {
			l.warnf("server %s: %d clients dropped by allowed_clients in the last minute", info.Name, delta)
		} else if delta > 0 {
			l.logf("server %s: %d clients dropped by allowed_clients in the last minute", info.Name, delta)
		}
	}
	for name := range l.serverPrevDrops {
		if !present[name] {
			delete(l.serverPrevDrops, name)
		}
	}
}

func (l *liveAngel) flushServerDiagnosticsNow() {
	l.mu.Lock()
	inst := l.inst
	l.mu.Unlock()
	if source, ok := inst.(serverInfoSource); ok {
		l.flushServerDiagnostics(source)
	}
}

func (l *liveAngel) serverDiagnosticsLoop(ctx context.Context) {
	ticker := time.NewTicker(serverDiagnosticsEvery)
	defer ticker.Stop()
	injected := l.serverTickerC
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.flushServerDiagnosticsNow()
		case _, ok := <-injected:
			if injected == nil || !ok {
				injected = nil
				continue
			}
			l.flushServerDiagnosticsNow()
		}
	}
}
