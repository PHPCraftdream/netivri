package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
)

// runStatus reports the live angel state; exit 4 (ANGEL_NOT_RUNNING)
// when nothing is running.
func runStatus(args []string, stdout io.Writer) error {
	home, homeSource, rest, err := parseHomeArgsFrom("status", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	full := fs.Bool("full", false, "print the full status with warm nodes, stats and affinity")
	watch := fs.Bool("watch", false, "keep running and print who is active every interval")
	var intervalFlag watchIntervalFlag
	fs.Var(&intervalFlag, "interval", "how often --watch polls, e.g. 2s (default 2s)")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("status: %v", err)
	}
	if len(pos) > 0 {
		return cli.Usage("status: unexpected argument %q", pos[0])
	}
	if *watch && *full {
		return cli.Usage("status: --watch and --full cannot be combined; --full is for the one-shot answer")
	}
	if intervalFlag.set && !*watch {
		return cli.Usage("status: --interval needs --watch")
	}
	if *watch {
		interval := statusWatchDefault
		if intervalFlag.set {
			if intervalFlag.value == "" {
				return cli.Usage("status: --interval must not be empty")
			}
			var err error
			interval, err = parseWatchInterval(intervalFlag.value)
			if err != nil {
				return cli.Usage("status: %v", err)
			}
		}
		return runStatusWatch(home, interval, wantsJSON(args, os.Getenv))
	}
	ctx, cancel := context.WithTimeout(context.Background(), angel.ProbeTimeout)
	defer cancel()
	_, st, err := angelProbeLive(ctx, home)
	if errors.Is(err, angel.ErrNotRunning) {
		return &cli.Error{
			Code:    "ANGEL_NOT_RUNNING",
			Message: "angel is not running",
			Hint:    "start it with: netivri up",
			Exit:    cli.ExitNoAngel,
		}
	}
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	accounts := append([]angel.AccountInfo{}, st.Accounts...)
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
	data["angel"] = "running"
	data["home"] = home
	data["home_source"] = homeSource
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
