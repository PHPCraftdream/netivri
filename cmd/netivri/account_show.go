package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/stats"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// accountShowTier is one (country, provider group) pair of an account's
// route, in the order the daemon tries them. A pair without nodes is shown
// as skipped, with a reason, and does not consume a tier index.
type accountShowTier struct {
	Tier      int               `json:"tier"`
	Country   string            `json:"country"`
	Providers []string          `json:"providers"`
	Skipped   bool              `json:"skipped,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Nodes     []accountShowNode `json:"nodes"`
}

// accountShowNode is one node of a tier: its static identity and, when the
// daemon runs, its live probe and traffic state (null otherwise).
type accountShowNode struct {
	Label       string `json:"label"`
	Name        string `json:"name,omitempty"` // display name from the subscription
	Provider    string `json:"provider"`
	Country     string `json:"country"`
	Tier        int    `json:"tier"`
	Alive       *bool  `json:"alive"`
	DelayMS     *int64 `json:"delay_ms"`
	Connections *int   `json:"connections"`
}

// accountShow implements `account show`: the configured lists, the route as
// ordered tiers, and, with a running daemon, the live probe and traffic state
// of every node. It never prints a password, a hash or an address.
func accountShow(home string, cfg *config.Config, rest []string, stdout io.Writer) error {
	if len(rest) != 1 {
		return cli.Usage("account show: requires ACCOUNT")
	}
	login := rest[0]
	if !config.ValidAccountLogin(login) {
		return &cli.Error{Code: "ACCOUNT_INVALID", Message: "invalid account login", Exit: cli.ExitUsage}
	}
	a, _ := cfg.Account(login)
	if a == nil {
		return accountNotFound()
	}
	providers, countries := routeLists(*a)
	tiers, err := accountShowTiers(home, cfg, providers, countries)
	if err != nil {
		return err
	}
	live := accountShowLive(context.Background(), home, login, tiers)
	cli.Success(stdout, map[string]any{
		"login":        a.Login,
		"provider":     a.Provider,
		"exit_country": a.ExitCountry,
		"providers":    providers,
		"countries":    countries,
		"tiers":        tiers,
		"live":         live,
	})
	return nil
}

// accountShowTiers loads the stored snapshots the account's route reads and
// builds its tiers. Pairs whose provider is not configured or whose snapshot
// is not stored are skipped with a reason; a snapshot that exists but cannot
// be read is an error, as elsewhere.
func accountShowTiers(home string, cfg *config.Config, providers, countries []string) ([]accountShowTier, error) {
	if len(providers) == 0 && len(countries) == 0 {
		return []accountShowTier{}, nil
	}
	var groups [][]string
	if len(providers) > 0 {
		for _, name := range providers {
			groups = append(groups, []string{name})
		}
	} else {
		var all []string
		for _, s := range cfg.Subscriptions {
			all = append(all, s.Name)
		}
		sort.Strings(all)
		groups = [][]string{all}
	}
	known := make(map[string]bool, len(cfg.Subscriptions))
	for _, s := range cfg.Subscriptions {
		known[s.Name] = true
	}
	snapshots := make(map[string]*subscription.Snapshot)
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	for _, names := range groups {
		for _, name := range names {
			if !known[name] {
				continue
			}
			if _, ok := snapshots[name]; ok {
				continue
			}
			snap, err := store.Load(name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, snapshotError(name)
			}
			snapshots[name] = snap
		}
	}
	return buildShowTiers(known, snapshots, groups, countries), nil
}

// buildShowTiers mirrors buildAccountRoutePool: the countries in list order,
// inside a country the provider groups in list order, one tier per pair with
// nodes; a pair without nodes is skipped and does not consume an index.
func buildShowTiers(known map[string]bool, snapshots map[string]*subscription.Snapshot, groups [][]string, countries []string) []accountShowTier {
	out := []accountShowTier{}
	if len(countries) == 0 {
		countries = []string{config.AnyCountry}
	}
	named := make(map[string]bool, len(countries))
	for _, c := range countries {
		named[c] = true
	}
	tier := 0
	for _, country := range countries {
		for _, names := range groups {
			t := accountShowTier{Tier: tier, Country: country, Providers: names, Nodes: []accountShowNode{}}
			reason := ""
			type pair struct{ provider, id, country, name string }
			var nodes []pair
			for _, name := range names {
				if !known[name] {
					reason = "provider " + name + " is not a configured subscription"
					continue
				}
				snap := snapshots[name]
				if snap == nil {
					reason = "snapshot of provider " + name + " is not stored; run netivri sub update"
					continue
				}
				for _, n := range snap.Nodes {
					if country == config.AnyCountry {
						if named[n.Country] {
							continue
						}
					} else if n.Country != country {
						continue
					}
					nodes = append(nodes, pair{name, n.ID, n.Country, stats.SanitizeName(n.Name)})
				}
			}
			sort.Slice(nodes, func(i, j int) bool {
				if nodes[i].id != nodes[j].id {
					return nodes[i].id < nodes[j].id
				}
				return nodes[i].provider < nodes[j].provider
			})
			if len(nodes) == 0 {
				if reason == "" {
					reason = "no nodes for this country and provider"
				}
				t.Skipped, t.Reason = true, reason
				out = append(out, t)
				continue
			}
			for _, n := range nodes {
				t.Nodes = append(t.Nodes, accountShowNode{
					Label:    nodeLabel(n.provider, n.id),
					Name:     n.name,
					Provider: n.provider,
					Country:  n.country,
					Tier:     tier,
				})
			}
			out = append(out, t)
			tier++
		}
	}
	return out
}

// accountShowLive fills the live fields of the tiers from the running
// daemon's status and reports whether it could. Without a daemon the static
// part stands and the live fields stay null.
func accountShowLive(ctx context.Context, home, login string, tiers []accountShowTier) bool {
	ctx, cancel := context.WithTimeout(ctx, daemon.ProbeTimeout)
	defer cancel()
	_, st, err := daemonProbeLive(ctx, home)
	if err != nil {
		return false
	}
	health := make(map[string]daemon.NodeHealth)
	if st.Warm != nil {
		for _, n := range st.Warm.Nodes {
			if n.Label != "" {
				health[n.Label] = n
			}
		}
	}
	uses := make(map[string]int)
	tracked := false
	for _, a := range st.Activity {
		if a.Login != login {
			continue
		}
		tracked = true
		for _, n := range a.Nodes {
			uses[n.Node] += n.Connections
		}
	}
	for ti := range tiers {
		for ni := range tiers[ti].Nodes {
			n := &tiers[ti].Nodes[ni]
			if h, ok := health[n.Label]; ok {
				alive, delay := h.Alive, h.DelayMS
				n.Alive, n.DelayMS = &alive, &delay
			}
			if c, ok := uses[n.Label]; ok {
				n.Connections = &c
			} else if tracked {
				// The daemon lists every configured account: an idle node
				// reports 0, not null.
				zero := 0
				n.Connections = &zero
			}
		}
	}
	return true
}
