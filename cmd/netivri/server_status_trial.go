package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

// overlayServerRuntimeCounters copies runtime-only counters for configured servers.
// A missing or unreachable angel deliberately leaves the configuration view intact.
func overlayServerRuntimeCounters(home string, views []serverView) {
	ctx, cancel := context.WithTimeout(context.Background(), angel.ProbeTimeout)
	defer cancel()
	_, status, err := angelProbeLive(ctx, home)
	if err != nil {
		return
	}
	byName := make(map[string]angel.ServerInfo, len(status.Servers))
	for _, info := range status.Servers {
		byName[info.Name] = info
	}
	for i := range views {
		if info, ok := byName[views[i].Name]; ok {
			views[i].Unclassified = info.Unclassified
			views[i].SlowFirstByte = info.SlowFirstByte
			views[i].Replayed = info.Replayed
			views[i].Ambiguous = info.Ambiguous
		}
	}
}

func serverCounterChecks(status angel.Status, configured []servers.Spec) []doctorCheck {
	configuredNames := make(map[string]bool, len(configured))
	for _, spec := range configured {
		configuredNames[spec.Name] = true
	}
	checks := make([]doctorCheck, 0, len(status.Servers))
	for _, info := range status.Servers {
		if configuredNames[info.Name] {
			if detail := serverCounterDetail(info); detail != "" {
				checks = append(checks, doctorCheck{"server-counters-" + info.Name, doctorOK, detail})
			}
		}
	}
	return checks
}

func serverCounterDetail(info angel.ServerInfo) string {
	var parts []string
	if info.Unclassified != 0 {
		parts = append(parts, "unclassified="+strconv.FormatUint(info.Unclassified, 10))
	}
	if info.SlowFirstByte != 0 {
		parts = append(parts, "slow_first_byte="+strconv.FormatUint(info.SlowFirstByte, 10))
	}
	if info.Replayed != 0 {
		parts = append(parts, "replayed="+strconv.FormatUint(info.Replayed, 10))
	}
	if info.Ambiguous != 0 {
		parts = append(parts, "ambiguous="+strconv.FormatUint(info.Ambiguous, 10))
	}
	return strings.Join(parts, ", ")
}
