package main

import (
	"path/filepath"
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// statsFile is the node statistics database inside the state directory.
const statsFile = "stats.db"

func statsPath(home string) string {
	return filepath.Join(home, statsFile)
}

// statsObserver adapts the statistics store to core.Observer.
type statsObserver struct {
	store *stats.Store
}

var _ core.Observer = statsObserver{}

func (o statsObserver) ObserveProbe(label string, alive bool, delay time.Duration) {
	o.store.Record(stats.Sample{
		At:    time.Now(),
		Label: label,
		Kind:  stats.KindProbe,
		OK:    alive,
		Delay: delay,
	})
}

func (o statsObserver) ObserveDial(label string, ok bool, took time.Duration) {
	o.store.Record(stats.Sample{
		At:    time.Now(),
		Label: label,
		Kind:  stats.KindDial,
		OK:    ok,
		Delay: took,
	})
}

func (o statsObserver) ObserveThroughput(label string, bytes int64, window time.Duration) {
	o.store.Record(stats.Sample{
		At:    time.Now(),
		Label: label,
		Kind:  stats.KindThroughput,
		OK:    true,
		Delay: window,
		Bytes: bytes,
	})
}

// openStats opens the statistics store for the nodes of the routes. Without
// nodes there is nothing to record and no file is created. A store that
// cannot be opened is reported through logf and nil is returned: the proxy
// never fails because of its bookkeeping.
func openStats(home string, infos []stats.NodeInfo, logf func(format string, args ...any)) *stats.Store {
	if len(infos) == 0 {
		return nil
	}
	store, err := stats.Open(stats.Options{Path: statsPath(home)})
	if err != nil {
		logf("statistics disabled: %v", err)
		return nil
	}
	if err := store.SetNodes(infos); err != nil {
		logf("statistics: node details not stored: %v", err)
	}
	return store
}

// closeStats flushes and closes the store; it is idempotent and nil-safe.
func closeStats(store *stats.Store) {
	if store != nil {
		_ = store.Close()
	}
}

// statsInfo is the `stats` part of the status answer; nil without a store.
func statsInfo(store *stats.Store, home string) *daemon.StatsInfo {
	if store == nil {
		return nil
	}
	return &daemon.StatsInfo{Path: statsPath(home), Dropped: store.Dropped()}
}

// affinityInfo is the `affinity` part of the status answer; nil when no
// account has a route.
func affinityInfo(inst *core.Instance, routed bool) *daemon.AffinityInfo {
	if !routed {
		return nil
	}
	s := inst.AffinityStats()
	return &daemon.AffinityInfo{
		Bindings:  s.Bindings,
		Rebinds:   s.Rebinds,
		Hedges:    s.Hedges,
		HedgeWins: s.HedgeWins,
	}
}
