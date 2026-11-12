package main

import (
	"sort"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/core"
)

// liveHopPoolSource is the optional pool view of a core instance.
type liveHopPoolSource interface {
	SnapshotHopPools() []core.HopPoolSnapshot
	ExportHopPools() []core.HopPoolState
}

// liveHopStatusClock lets a core fake pin the time of revival countdowns.
type liveHopStatusClock interface {
	HopStatusNow() time.Time
}

type liveHopPoolKey struct {
	role, route, identity string
}

type liveHopStatusEntry struct {
	state     string
	trips     int
	nextProbe time.Time
	hasExport bool
}

// addLiveHopPoolStatus fills AccountInfo.HopPools for every account whose route
// has a role with several hops, and Status.HopPools with the pools of the global
// default route (core.GlobalPoolRoute). Pools are told apart by role and pool
// identity, so two providers behind one route never merge; accounts sharing a
// route get the same data. Hops show only their short opaque id.
func addLiveHopPoolStatus(st *angel.Status, routes routePool, inst coreInstance) {
	source, ok := inst.(liveHopPoolSource)
	if !ok || st == nil {
		return
	}
	now := time.Now()
	if clock, ok := inst.(liveHopStatusClock); ok {
		now = clock.HopStatusNow()
	}

	loginsByRoute := make(map[string][]int, len(routes.accountRoutes))
	for i := range st.Accounts {
		if route, ok := routes.accountRoutes[st.Accounts[i].Login]; ok && route != core.GlobalPoolRoute {
			loginsByRoute[route] = append(loginsByRoute[route], i)
		}
	}
	infos := liveHopPoolInfos(source, now, func(route string) bool {
		return route == core.GlobalPoolRoute || len(loginsByRoute[route]) > 0
	})
	for route, indexes := range loginsByRoute {
		for _, index := range indexes {
			st.Accounts[index].HopPools = append(st.Accounts[index].HopPools, infos[route]...)
		}
	}
	st.HopPools = infos[core.GlobalPoolRoute]
}

// liveHopPoolInfos groups the snapshots of the wanted routes into pool views,
// pre pools before post pools, keeping only pools of several hops.
func liveHopPoolInfos(source liveHopPoolSource, now time.Time, wanted func(route string) bool) map[string][]angel.HopPoolInfo {
	pools := make(map[liveHopPoolKey]map[string]liveHopStatusEntry)
	for _, snapshot := range source.SnapshotHopPools() {
		if !validHopHealthRole(snapshot.Role) || !validHopHealthKey(snapshot.PoolIdentity) || snapshot.RouteKey == "" || !wanted(snapshot.RouteKey) {
			continue
		}
		state := snapshot.State
		switch state {
		case "healthy":
			state = "active"
		case "quarantined", "probing":
		default:
			continue
		}
		group := liveHopPoolKey{role: snapshot.Role, route: snapshot.RouteKey, identity: snapshot.PoolIdentity}
		if pools[group] == nil {
			pools[group] = make(map[string]liveHopStatusEntry)
		}
		if _, exists := pools[group][snapshot.Key]; !exists {
			pools[group][snapshot.Key] = liveHopStatusEntry{state: state, trips: snapshot.Trips}
		}
	}

	for _, exported := range source.ExportHopPools() {
		if !validHopHealthRole(exported.Role) || !validHopHealthKey(exported.PoolIdentity) {
			continue
		}
		group := liveHopPoolKey{role: exported.Role, route: exported.RouteKey, identity: exported.PoolIdentity}
		entries, exists := pools[group]
		if !exists {
			continue
		}
		for _, entry := range exported.Entries {
			if entry.Trips < 0 {
				continue
			}
			observed, ok := entries[entry.Key]
			if !ok || (observed.state != "quarantined" && observed.state != "probing") {
				continue
			}
			observed.nextProbe, observed.hasExport = entry.NextProbe, true
			entries[entry.Key] = observed
		}
	}

	groupsByRoute := make(map[string][]liveHopPoolKey)
	for group, entries := range pools {
		if len(entries) > 1 {
			groupsByRoute[group.route] = append(groupsByRoute[group.route], group)
		}
	}
	out := make(map[string][]angel.HopPoolInfo, len(groupsByRoute))
	for route, groups := range groupsByRoute {
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].role != groups[j].role {
				return groups[i].role == "pre"
			}
			return groups[i].identity < groups[j].identity
		})
		for _, group := range groups {
			entries := pools[group]
			keys := make([]string, 0, len(entries))
			for key := range entries {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			info := angel.HopPoolInfo{Role: group.role, HopCount: len(keys), Hops: make([]angel.HopInfo, 0, len(keys))}
			for _, key := range keys {
				entry := entries[key]
				hop := angel.HopInfo{State: entry.state, Trips: entry.trips}
				if validHopHealthKey(key) {
					hop.ID = key[:8]
				} else {
					info.UnpersistableCount++
				}
				switch entry.state {
				case "active":
					info.ActiveCount++
				case "quarantined":
					info.QuarantinedCount++
					if entry.hasExport {
						seconds := revivalSeconds(entry.nextProbe, now)
						hop.NextRevivalSeconds = &seconds
					}
				case "probing":
					info.QuarantinedCount++
				}
				info.Hops = append(info.Hops, hop)
			}
			out[route] = append(out[route], info)
		}
	}
	return out
}

// revivalSeconds rounds the wait for the next probe up to whole seconds.
func revivalSeconds(next time.Time, now time.Time) int64 {
	remaining := next.Sub(now)
	if remaining <= 0 {
		return 0
	}
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	return seconds
}
