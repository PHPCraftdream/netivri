package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// list implements `sub list`. The subscription URL and the via proxy are
// secrets: only the host of the URL and a via_set boolean are reported.
func (a *subApp) list() error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	store := a.store()
	subs := make([]map[string]any, 0, len(cfg.Subscriptions))
	for _, s := range cfg.Subscriptions {
		entry := map[string]any{
			"name":       s.Name,
			"host":       urlHost(s.URL),
			"via_set":    s.Via != "",
			"nodes":      nil,
			"fetched_at": nil,
		}
		snap, err := store.Load(s.Name)
		switch {
		case err == nil:
			entry["nodes"] = len(snap.Nodes)
			entry["fetched_at"] = snap.FetchedAt.Format(time.RFC3339)
		case errors.Is(err, os.ErrNotExist):
			// Never fetched: nodes and fetched_at stay null.
		default:
			return snapshotError(s.Name)
		}
		subs = append(subs, entry)
	}
	cli.Success(a.stdout, map[string]any{"subscriptions": subs})
	return nil
}

// snapshotError reports a stored snapshot that exists but cannot be read
// (corrupt or unreadable); a missing snapshot is not an error. The
// subscription name is a label, never the URL.
func snapshotError(name string) *cli.Error {
	return &cli.Error{
		Code:    "SUB_SNAPSHOT_INVALID",
		Message: fmt.Sprintf("the stored snapshot of subscription %q cannot be read", name),
		Hint:    "run `netivri sub update` to fetch it again",
		Exit:    cli.ExitInternal,
	}
}

// urlHost returns the bare hostname of a subscription URL; anything
// unparsable becomes "" (never the raw URL).
func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// rm implements `sub rm N`: removes the subscription from the config and
// deletes its stored snapshot (a missing snapshot is fine).
func (a *subApp) rm(args []string) error {
	fs := flag.NewFlagSet("sub rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("home", "", "state directory (resolved by runSub)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage("sub rm: %v", err)
	}
	if len(pos) != 1 {
		return cli.Usage("sub rm: exactly one subscription name is required")
	}
	name := pos[0]

	cfg, err := a.load()
	if err != nil {
		return err
	}
	idx := -1
	for i, s := range cfg.Subscriptions {
		if s.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
	}
	cfg.Subscriptions = append(cfg.Subscriptions[:idx], cfg.Subscriptions[idx+1:]...)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(a.configPath(), cfg); err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "save config: " + err.Error(), Exit: cli.ExitInternal}
	}
	if err := a.store().Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &cli.Error{Code: "INTERNAL", Message: "remove stored subscription: " + err.Error(), Exit: cli.ExitInternal}
	}
	cli.Success(a.stdout, map[string]any{"removed": name})
	return nil
}
