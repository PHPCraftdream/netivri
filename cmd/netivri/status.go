package main

import (
	"context"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/daemon"
)

// runStatus reports the live daemon state; exit 4 (DAEMON_NOT_RUNNING)
// when nothing is running.
func runStatus(args []string, stdout io.Writer) error {
	home, rest, err := parseHomeArgs("status", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return cli.Usage("status: unexpected argument %q", rest[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemon.ProbeTimeout)
	defer cancel()
	_, st, err := daemonProbeLive(ctx, home)
	if errors.Is(err, daemon.ErrNotRunning) {
		return &cli.Error{
			Code:    "DAEMON_NOT_RUNNING",
			Message: "daemon is not running",
			Hint:    "start it with: netivri up",
			Exit:    cli.ExitNoDaemon,
		}
	}
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	accounts := append([]daemon.AccountInfo{}, st.Accounts...)
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Login < accounts[j].Login })
	data := map[string]any{
		"daemon":         "running",
		"foreground":     st.Foreground,
		"pid":            st.PID,
		"listen":         st.Listen,
		"protocols":      st.Protocols,
		"auth":           st.Auth,
		"route":          st.Route,
		"started_at":     st.StartedAt,
		"uptime_seconds": uptimeSeconds(st.StartedAt),
		"version":        st.Version,
		"accounts":       accounts,
	}
	if st.Warm != nil {
		data["warm"] = st.Warm
	}
	if st.Stats != nil {
		data["stats"] = st.Stats
	}
	if st.Affinity != nil {
		data["affinity"] = st.Affinity
	}
	if st.Refresh != nil {
		data["refresh"] = st.Refresh
	}
	if st.Reload != nil {
		data["reload"] = st.Reload
	}
	cli.Success(stdout, data)
	return nil
}

// uptimeSeconds parses an RFC3339 UTC timestamp and returns the whole
// seconds elapsed since it (0 when it cannot be parsed or is in the
// future).
func uptimeSeconds(startedAt string) int64 {
	t, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return 0
	}
	d := time.Since(t)
	if d <= 0 {
		return 0
	}
	return int64(d.Seconds())
}
