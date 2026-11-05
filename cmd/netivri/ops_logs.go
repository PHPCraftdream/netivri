package main

import (
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
)

// The log reading of `netivri logs`, shared with the Logs tab of the shell
// (cmd/netivri/tui_logs.go): one query builder and one read for both surfaces,
// so they cannot drift apart (docs/design/parity.md).

// opsLogsQuery builds the log query of `netivri logs`: INFO and above unless
// another level is asked for, and only since the given window when one is.
func opsLogsQuery(level, since string, tail int, now func() time.Time) (angel.LogQuery, error) {
	q := angel.LogQuery{Tail: tail, MinLevel: angel.LevelInfo}
	if level != "" {
		l, ok := angel.NormalizeLevel(level)
		if !ok {
			return angel.LogQuery{}, cli.Usage("logs: --level must be debug, info, warn or error")
		}
		q.MinLevel = l
	}
	if since != "" {
		window, err := parseWindowFor("logs", since)
		if err != nil {
			return angel.LogQuery{}, err
		}
		if now == nil {
			now = time.Now
		}
		q.Since = now().Add(-window)
	}
	return q, nil
}

// opsLogsRead reads the entries of the angel log that match q and the offset
// a follow continues from.
func opsLogsRead(home string, q angel.LogQuery) ([]angel.LogEntry, int64, error) {
	return angel.ReadLog(home, q)
}
