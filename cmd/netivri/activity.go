package main

import (
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// activityInfo is the `activity` part of the status answer: one entry per
// configured account, in the order of accounts, with its open connections and
// the nodes, providers and countries they use. infos tells which provider and
// country a node label belongs to; the providers and countries are listed
// busiest first, each once.
func activityInfo(accounts []angel.AccountInfo, live []core.AccountActivity, infos []stats.NodeInfo) []angel.AccountActivity {
	nodeInfo := make(map[string]stats.NodeInfo, len(infos))
	for _, n := range infos {
		nodeInfo[n.Label] = n
	}
	byLogin := make(map[string]core.AccountActivity, len(live))
	for _, a := range live {
		byLogin[a.Login] = a
	}
	out := make([]angel.AccountActivity, 0, len(accounts))
	for _, acc := range accounts {
		a := byLogin[acc.Login]
		item := angel.AccountActivity{
			Login:         acc.Login,
			Active:        a.Connections > 0,
			Connections:   a.Connections,
			UploadBytes:   a.Upload,
			DownloadBytes: a.Download,
			Providers:     []string{},
			Countries:     []string{},
			Nodes:         make([]angel.NodeUse, 0, len(a.Nodes)),
		}
		seenProvider := make(map[string]bool)
		seenCountry := make(map[string]bool)
		for _, n := range a.Nodes {
			use := angel.NodeUse{
				Node:          n.Label,
				Connections:   n.Connections,
				UploadBytes:   n.Upload,
				DownloadBytes: n.Download,
			}
			if info, ok := nodeInfo[n.Label]; ok {
				use.Provider, use.Country, use.Name = info.Provider, info.Country, info.Name
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
