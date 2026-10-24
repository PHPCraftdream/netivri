package main

import (
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// activityInfo is the `activity` part of the status answer: one entry per
// configured account, in the order of accounts, with its open connections and
// the nodes, providers and countries they use. infos tells which provider and
// country a node label belongs to; the providers and countries are listed
// busiest first, each once.
func activityInfo(accounts []daemon.AccountInfo, live []core.AccountActivity, infos []stats.NodeInfo) []daemon.AccountActivity {
	nodeInfo := make(map[string]stats.NodeInfo, len(infos))
	for _, n := range infos {
		nodeInfo[n.Label] = n
	}
	byLogin := make(map[string]core.AccountActivity, len(live))
	for _, a := range live {
		byLogin[a.Login] = a
	}
	out := make([]daemon.AccountActivity, 0, len(accounts))
	for _, acc := range accounts {
		a := byLogin[acc.Login]
		item := daemon.AccountActivity{
			Login:         acc.Login,
			Active:        a.Connections > 0,
			Connections:   a.Connections,
			UploadBytes:   a.Upload,
			DownloadBytes: a.Download,
			Providers:     []string{},
			Countries:     []string{},
			Nodes:         make([]daemon.NodeUse, 0, len(a.Nodes)),
		}
		seenProvider := make(map[string]bool)
		seenCountry := make(map[string]bool)
		for _, n := range a.Nodes {
			use := daemon.NodeUse{
				Node:          n.Label,
				Connections:   n.Connections,
				UploadBytes:   n.Upload,
				DownloadBytes: n.Download,
			}
			if info, ok := nodeInfo[n.Label]; ok {
				use.Provider, use.Country = info.Provider, info.Country
			}
			if use.Provider != "" && !seenProvider[use.Provider] {
				seenProvider[use.Provider] = true
				item.Providers = append(item.Providers, use.Provider)
			}
			if use.Country != "" && !seenCountry[use.Country] {
				seenCountry[use.Country] = true
				item.Countries = append(item.Countries, use.Country)
			}
			item.Nodes = append(item.Nodes, use)
		}
		out = append(out, item)
	}
	return out
}

// nodeHealthLogger returns the core.Options.OnNodeHealth hook that writes a
// WARN line when a route node goes down and an INFO line when it is back, with
// the node label, provider and country (never an address or a URL). Without
// route nodes there is nothing to report and it returns nil.
func nodeHealthLogger(infos []stats.NodeInfo, info, warn func(format string, args ...any)) func(label string, down bool) {
	if len(infos) == 0 {
		return nil
	}
	byLabel := make(map[string]stats.NodeInfo, len(infos))
	for _, n := range infos {
		byLabel[n.Label] = n
	}
	return func(label string, down bool) {
		n := byLabel[label]
		country := n.Country
		if country == "" {
			country = unknownCountry
		}
		if down {
			warn("node %s (provider %s, country %s) is down", label, n.Provider, country)
			return
		}
		info("node %s (provider %s, country %s) is back", label, n.Provider, country)
	}
}
