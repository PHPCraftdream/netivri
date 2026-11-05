package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// The node operations shared by `netivri node list` and the shell's Nodes
// dialog (docs/design/parity.md): the shell runs the same code the command
// runs, so the result is the same by construction.

// NodeEntry is one stored node as `netivri node list` reports it. Server and
// Port exist for the command's output only: the shell shows label, country
// and name and never an address or a port.
type NodeEntry struct {
	ID, Name, Type, Server string
	Port                   int
	Country, Sub           string
}

// Label is the node label of `diag dial --node`: {PROVIDER}/{NODE_ID}.
func (n NodeEntry) Label() string { return nodeLabel(n.Sub, n.ID) }

// NodeList lists the stored nodes of one or every subscription, in the stable
// order of the command: subscription order, then stored order. sub selects one
// subscription (SUB_NOT_FOUND when it does not exist); country filters by ISO
// 3166-1 alpha-2 code, case-insensitively. A subscription that was never
// fetched is skipped, like in the command.
func NodeList(home, sub, country string) ([]NodeEntry, error) {
	cfg, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	var subs []config.Subscription
	if sub != "" {
		for _, s := range cfg.Subscriptions {
			if s.Name == sub {
				subs = append(subs, s)
				break
			}
		}
		if subs == nil {
			return nil, &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
		}
	} else {
		subs = cfg.Subscriptions
	}
	country = strings.ToUpper(country)
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	out := make([]NodeEntry, 0)
	for _, s := range subs {
		snap, err := store.Load(s.Name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // never fetched yet
			}
			return nil, snapshotError(s.Name)
		}
		for _, n := range snap.Nodes {
			if country != "" && n.Country != country {
				continue
			}
			out = append(out, NodeEntry{ID: n.ID, Name: n.Name, Type: n.Type, Server: n.Server, Port: n.Port, Country: n.Country, Sub: s.Name})
		}
	}
	return out, nil
}
