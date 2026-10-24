package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/daemon"
)

// defaultLogTail is how many entries `logs` shows when --tail is not given.
const defaultLogTail = 100

// logsContext ends a followed log; tests replace it.
var logsContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runLogs implements `logs`: the daemon log of the state directory. It reads
// the file, so it works while the daemon runs and after it stopped.
func runLogs(args []string, stdout io.Writer) error {
	home, rest, err := parseHomeArgs("logs", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	follow := fs.Bool("follow", false, "keep running and print new entries")
	levelFlag := fs.String("level", "", "only this level and above: debug, info, warn or error")
	since := fs.String("since", "", "only the last period, e.g. 10m, 24h or 7d")
	tail := fs.Int("tail", defaultLogTail, "at most this many of the newest entries, 0 for all")
	pos, err := parseFlags(fs, rest)
	if err != nil {
		return cli.Usage("logs: %v", err)
	}
	if len(pos) > 0 {
		return cli.Usage("logs: unexpected argument %q", pos[0])
	}
	if *tail < 0 {
		return cli.Usage("logs: --tail must not be negative")
	}
	q := daemon.LogQuery{Tail: *tail}
	if *levelFlag != "" {
		level, ok := daemon.NormalizeLevel(*levelFlag)
		if !ok {
			return cli.Usage("logs: --level must be debug, info, warn or error")
		}
		q.MinLevel = level
	}
	if *since != "" {
		window, err := parseWindowFor("logs", *since)
		if err != nil {
			return err
		}
		q.Since = time.Now().Add(-window)
	}

	entries, offset, err := daemon.ReadLog(home, q)
	if errors.Is(err, os.ErrNotExist) {
		if *follow {
			// The daemon may not have started yet: wait for its log.
			offset = 0
		} else {
			return &cli.Error{
				Code:    "LOG_NOT_FOUND",
				Message: "there is no daemon log yet",
				Hint:    "the daemon writes it once started with: netivri up",
				Exit:    cli.ExitNotFound,
			}
		}
	} else if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "read log: " + err.Error(), Exit: cli.ExitInternal}
	}

	if !*follow {
		items := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			items = append(items, logItem(e))
		}
		cli.Success(stdout, map[string]any{"entries": items})
		return nil
	}

	emit := logLineWriter(stdout)
	if wantsJSON(args, os.Getenv) {
		emit = logJSONWriter(stdout)
	}
	for _, e := range entries {
		emit(e)
	}
	ctx, cancel := logsContext()
	defer cancel()
	q.Tail = 0 // the backlog is shown; everything new follows
	daemon.FollowLog(ctx, home, offset, q, emit)
	return nil
}

// logItem is the JSON/Ktav form of an entry.
func logItem(e daemon.LogEntry) map[string]any {
	return map[string]any{"time": logTime(e), "level": e.Level, "message": e.Message}
}

func logTime(e daemon.LogEntry) string {
	if e.Time.IsZero() {
		return ""
	}
	return e.Time.UTC().Format(time.RFC3339)
}

// logLineWriter prints an entry as the line it has in the log file.
func logLineWriter(w io.Writer) func(daemon.LogEntry) {
	return func(e daemon.LogEntry) {
		_, _ = fmt.Fprintln(w, daemon.FormatLogLine(e.Time, e.Level, e.Message))
	}
}

// logJSONWriter prints an entry as one JSON object per line (NDJSON).
func logJSONWriter(w io.Writer) func(daemon.LogEntry) {
	enc := json.NewEncoder(w)
	return func(e daemon.LogEntry) { _ = enc.Encode(logItem(e)) }
}
