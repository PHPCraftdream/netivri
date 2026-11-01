package core

import (
	"sort"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

// Labels of the connections that left through no route node: the account has
// no route and goes direct, or the instance routes everything through its one
// node.
const (
	ActivityDirect = "direct"
	ActivityNode   = "node"
)

// NodeActivity is the live traffic of one account through one node.
type NodeActivity struct {
	Label       string // node label, or ActivityDirect / ActivityNode
	Connections int
	Upload      int64 // bytes sent through the node by the open connections
	Download    int64
}

// AccountActivity is what one account is doing right now: its open
// connections, in total and per node.
type AccountActivity struct {
	Login       string
	Connections int
	Upload      int64
	Download    int64
	Nodes       []NodeActivity // most connections first, then by label
}

// Activity returns the open connections of every account that has any,
// sorted by login. Connections without a login (no authentication) are not
// attributed. It reads Mihomo's connection trackers and is safe to call while
// the instance runs. A connection opened before a Reconfigure keeps the
// label of the node it went through.
func (i *Instance) Activity() []AccountActivity {
	return collectActivity(managerSource{statistic.DefaultManager}, i.state().connLabels)
}

// collectActivity groups the trackers by account and node. labels maps the
// proxy name of a route member to its node label, as the throughput poller
// does.
func collectActivity(source trackerSource, labels map[string]string) []AccountActivity {
	type key struct{ login, label string }
	nodes := make(map[key]*NodeActivity)
	source.Range(func(t throughputTracker) bool {
		info := t.Info()
		if info == nil || info.Metadata == nil || info.Metadata.InUser == "" {
			return true
		}
		k := key{info.Metadata.InUser, activityLabel(info.Chain, labels)}
		n := nodes[k]
		if n == nil {
			n = &NodeActivity{Label: k.label}
			nodes[k] = n
		}
		n.Connections++
		n.Upload += info.UploadTotal.Load()
		n.Download += info.DownloadTotal.Load()
		return true
	})

	byLogin := make(map[string]*AccountActivity)
	for k, n := range nodes {
		a := byLogin[k.login]
		if a == nil {
			a = &AccountActivity{Login: k.login}
			byLogin[k.login] = a
		}
		a.Connections += n.Connections
		a.Upload += n.Upload
		a.Download += n.Download
		a.Nodes = append(a.Nodes, *n)
	}
	out := make([]AccountActivity, 0, len(byLogin))
	for _, a := range byLogin {
		sort.Slice(a.Nodes, func(x, y int) bool {
			if a.Nodes[x].Connections != a.Nodes[y].Connections {
				return a.Nodes[x].Connections > a.Nodes[y].Connections
			}
			return a.Nodes[x].Label < a.Nodes[y].Label
		})
		out = append(out, *a)
	}
	sort.Slice(out, func(x, y int) bool { return out[x].Login < out[y].Login })
	return out
}

// activityLabel names the node a connection chain left through.
func activityLabel(chain C.Chain, labels map[string]string) string {
	for _, name := range chain {
		if label, ok := labels[name]; ok {
			return label
		}
	}
	for _, name := range chain {
		if name == "DIRECT" || isRoutePostName(name) {
			return ActivityDirect
		}
	}
	return ActivityNode
}
