// Package stats stores node probe, dial and throughput samples in SQLite.
// See docs/design/node-stats.md.
package stats

import (
	// Pure-Go SQLite driver (registers "sqlite"); release builds use CGO_ENABLED=0.
	_ "modernc.org/sqlite"
)
