package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"sort"
	"strconv"
	"strings"
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
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	full := fs.Bool("full", false, "print the full status with warm nodes, stats and affinity")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("status: %v", err)
	}
	if len(pos) > 0 {
		return cli.Usage("status: unexpected argument %q", pos[0])
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
	raw, err := json.Marshal(st)
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	var data map[string]any
	if err := decodeUseNumber(raw, &data); err != nil {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	accRaw, err := json.Marshal(accounts)
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	var accs []any
	if err := decodeUseNumber(accRaw, &accs); err != nil {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	data["daemon"] = "running"
	data["allowed_clients"] = allowedClientsLabel(st.AllowedClients)
	data["accounts"] = accs
	data["uptime_seconds"] = uptimeSeconds(st.StartedAt)
	if st.Activity != nil {
		active := 0
		for _, a := range st.Activity {
			if a.Active {
				active++
			}
		}
		data["active_accounts"] = active
		data["activity"] = st.Activity
	}
	if !*full {
		delete(data, "stats")
		delete(data, "affinity")
		if st.Warm != nil {
			data["warm"] = map[string]any{
				"alive":          st.Warm.Alive,
				"total":          st.Warm.Total,
				"probes":         st.Warm.Probes,
				"probe_failures": st.Warm.ProbeFailures,
			}
		} else {
			delete(data, "warm")
		}
	}
	if st.Refresh != nil {
		data["refresh"] = st.Refresh
	}
	if st.Reload != nil {
		data["reload"] = st.Reload
	}
	normalizeNumbers(data)
	cli.Success(stdout, data)
	return nil
}

// decodeUseNumber decodes JSON so numbers keep their exact text
// (json.Number instead of float64).
func decodeUseNumber(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

// normalizeNumbers converts every json.Number in v to a plain Go number so
// that both output formats print the exact original digits.
func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeNumbers(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeNumbers(val)
		}
		return t
	case json.Number:
		return numberValue(t)
	default:
		return v
	}
}

// numberValue parses the exact literal of a json.Number: integer text becomes
// int64 (or uint64 when it overflows int64), everything else float64.
func numberValue(n json.Number) any {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return u
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return n
	}
	return f
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

// allowedClientsLabel renders the allowed_clients status value: an empty
// value (a state file written before the field existed) shows as "*".
func allowedClientsLabel(s string) string {
	if s == "" {
		return "*"
	}
	return s
}
