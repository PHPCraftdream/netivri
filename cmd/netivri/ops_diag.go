package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// The dial diagnostics shared by `netivri diag dial` and the shell's
// Diagnose action (docs/design/parity.md): the shell runs the same
// resolution and measurement as the command.

// diagDialNodes measures nodes with the same core call the command uses and
// maps a failure the way the command prints it: a *cli.Error keeps its code,
// anything else becomes DIAG_FAILED with the message of the core operation
// (fixed texts, no mapping contents).
func diagDialNodes(ctx context.Context, nodes []core.DiagNode, url string, samples int, idles []time.Duration) ([]core.NodeDiag, error) {
	results, err := diagDial(ctx, core.DiagOptions{
		Nodes:   nodes,
		URL:     url,
		Samples: samples,
		Idle:    idles,
	})
	if err != nil {
		var ce *cli.Error
		if errors.As(err, &ce) {
			return nil, ce
		}
		return nil, &cli.Error{Code: "DIAG_FAILED", Message: err.Error(), Exit: cli.ExitNetwork}
	}
	return results, nil
}

// DiagDialLabel measures one stored node by its label ({PROVIDER}/{NODE_ID}),
// through the same resolution the --node flag of the command uses. The idle
// survival test is skipped (the shell equivalent of --idle off) and the probe
// URL is the warm-up default. Errors carry the label, never an address.
func DiagDialLabel(ctx context.Context, home, label string, samples int) (core.NodeDiag, error) {
	if samples < 1 || samples > maxDiagSamples {
		return core.NodeDiag{}, cli.Usage("diag: --samples must be between 1 and %d", maxDiagSamples)
	}
	nd, err := findDiagNode(home, label)
	if err != nil {
		return core.NodeDiag{}, err
	}
	rs, err := diagDialNodes(ctx, []core.DiagNode{nd}, diagProbeURL, samples, []time.Duration{})
	if err != nil {
		return core.NodeDiag{}, err
	}
	if len(rs) == 0 {
		return core.NodeDiag{}, &cli.Error{Code: "DIAG_FAILED", Message: "the run produced no result", Exit: cli.ExitNetwork}
	}
	return rs[0], nil
}

// findDiagNode resolves a node label (<provider>/<node id>) against every
// configured subscription's stored snapshot. The error message carries the
// label only, never a server address.
func findDiagNode(home, label string) (core.DiagNode, error) {
	notFound := &cli.Error{Code: "NODE_NOT_FOUND", Message: "node not found: " + label, Exit: cli.ExitNotFound}
	provider, id, ok := strings.Cut(label, "/")
	if !ok || provider == "" || id == "" {
		return core.DiagNode{}, notFound
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return core.DiagNode{}, err
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	for _, sub := range cfg.Subscriptions {
		snap, err := store.Load(sub.Name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return core.DiagNode{}, snapshotError(sub.Name)
		}
		for _, n := range snap.Nodes {
			if nodeLabel(sub.Name, n.ID) == label {
				return core.DiagNode{Label: label, Mapping: n.Raw}, nil
			}
		}
	}
	return core.DiagNode{}, notFound
}
