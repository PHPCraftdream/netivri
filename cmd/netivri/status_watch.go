package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/daemon"
)

// statusWatch bounds of --interval.
const (
	statusWatchMinInterval = time.Second
	statusWatchMaxInterval = 60 * time.Second
	statusWatchDefault     = 2 * time.Second
	statusWatchMaxFailures = 3
	statusWatchMaxNodes    = 3
)

// statusWatchContext ends a watched status; tests replace it.
var statusWatchContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// statusWatchTerminal reports whether stdout is a terminal (a frame then
// clears the screen, otherwise frames are separated by a blank line).
var statusWatchTerminal = func() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// watchIntervalFlag records that --interval was given, even as an empty
// value (`--interval=`), which a plain string flag would hide.
type watchIntervalFlag struct {
	value string
	set   bool
}

func (f *watchIntervalFlag) String() string {
	if f == nil {
		return ""
	}
	return f.value
}

func (f *watchIntervalFlag) Set(s string) error {
	f.value = s
	f.set = true
	return nil
}

// parseWatchInterval validates the --interval value.
func parseWatchInterval(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--interval must be a duration, e.g. 2s: %v", err)
	}
	if d < statusWatchMinInterval || d > statusWatchMaxInterval {
		return 0, fmt.Errorf("--interval must be between 1s and 60s")
	}
	return d, nil
}

// watchOptions carries what the watch loop needs injectable for tests.
type watchOptions struct {
	interval time.Duration
	jsonOut  bool
	term     bool
	ticks    <-chan time.Time // nil: a real ticker at interval
	now      func() time.Time
}

// watchStatus polls the daemon every interval and prints one frame per tick.
// After the daemon was seen running, a poll that reports the state file gone
// or another pid behind it ends the watch with DAEMON_NOT_RUNNING (exit 4)
// after a final line; any other poll failure (a control timeout, for
// instance) is a warning and retried on the next tick, and
// statusWatchMaxFailures in a row end the watch with INTERNAL. The same
// retry rule covers a failed very first poll; a not-running daemon at the
// very first poll is the usual exit-4 error at once. Cancellation returns
// nil at a frame boundary.
func watchStatus(ctx context.Context, out io.Writer, poll func() (daemon.Status, error), o watchOptions) error {
	if o.now == nil {
		o.now = time.Now
	}
	ticks := o.ticks
	if ticks == nil {
		t := time.NewTicker(o.interval)
		defer t.Stop()
		ticks = t.C
	}
	w := watchPrinter{out: out, jsonOut: o.jsonOut, term: o.term}
	var st daemon.Status
	var err error
	var prev map[string]daemon.AccountActivity
	var prevAt time.Time
	frame := func() {
		now := o.now()
		rates := watchRates(prev, prevAt, now, st.Activity)
		w.frame(st, rates, now)
		prev = watchSnapshot(st.Activity)
		prevAt = now
	}
	seen := false
	failures := 0
	first := true
	for {
		if first {
			first = false
		} else {
			select {
			case <-ctx.Done():
				return nil
			case <-ticks:
			}
		}
		st, err = poll()
		// The poll context derives from ctx: a cancellation can surface
		// here as a poll error and is never a poll failure.
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			seen = true
			failures = 0
			frame()
			continue
		}
		if errors.Is(err, daemon.ErrNotRunning) {
			if !seen {
				return daemonNotRunning()
			}
			w.stopped(o.now())
			return daemonNotRunning()
		}
		failures++
		w.warn(err, o.now())
		if failures >= statusWatchMaxFailures {
			return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
		}
	}
}

// daemonNotRunning is the usual exit-4 error of `status`.
func daemonNotRunning() error {
	return &cli.Error{
		Code:    "DAEMON_NOT_RUNNING",
		Message: "daemon is not running",
		Hint:    "start it with: netivri up",
		Exit:    cli.ExitNoDaemon,
	}
}

// watchSnapshot copies the activity by login, so the next frame can diff it.
func watchSnapshot(activity []daemon.AccountActivity) map[string]daemon.AccountActivity {
	m := make(map[string]daemon.AccountActivity, len(activity))
	for _, a := range activity {
		m[a.Login] = a
	}
	return m
}

// watchRates computes per-login bytes per second from the delta between the
// two polls, over the real elapsed time. The first frame has no rates (nil);
// a negative delta counts as 0.
func watchRates(prev map[string]daemon.AccountActivity, prevAt, now time.Time, activity []daemon.AccountActivity) map[string][2]float64 {
	if prev == nil {
		return nil
	}
	elapsed := now.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return nil
	}
	rates := make(map[string][2]float64, len(activity))
	for _, a := range activity {
		p, ok := prev[a.Login]
		rate := [2]float64{}
		if ok {
			rate[0] = clampRate(float64(a.UploadBytes-p.UploadBytes) / elapsed)
			rate[1] = clampRate(float64(a.DownloadBytes-p.DownloadBytes) / elapsed)
		}
		rates[a.Login] = rate
	}
	return rates
}

func clampRate(r float64) float64 {
	if r < 0 {
		return 0
	}
	return r
}

// watchPrinter renders the frames, as an ASCII table or as NDJSON.
type watchPrinter struct {
	out     io.Writer
	jsonOut bool
	term    bool
	wrote   bool
}

// beginFrame is the text-mode frame boundary: a screen clear on a
// terminal, a blank separator line otherwise. JSON mode never calls it:
// there, the output is exactly one object per line.
func (w *watchPrinter) beginFrame() {
	if w.term {
		fmt.Fprint(w.out, "\x1b[H\x1b[2J")
	} else if w.wrote {
		fmt.Fprintln(w.out)
	}
	w.wrote = true
}

func (w *watchPrinter) frame(st daemon.Status, rates map[string][2]float64, now time.Time) {
	if w.jsonOut {
		w.frameJSON(st, rates, now)
		return
	}
	w.beginFrame()
	w.frameText(st, rates, now)
}

func (w *watchPrinter) frameText(st daemon.Status, rates map[string][2]float64, now time.Time) {
	accounts := watchSorted(st.Activity)
	active := 0
	for _, a := range accounts {
		if a.Active {
			active++
		}
	}
	fmt.Fprintf(w.out, "netivri status --watch  listen=%s  uptime=%s  active_accounts=%d\n",
		asciiSafe(st.Listen), watchUptime(st.StartedAt, now), active)
	rows := make([][]string, 0, len(accounts))
	for _, a := range accounts {
		up, down := "", "" // blank on the first frame
		if rates != nil {
			r, ok := rates[a.Login]
			if ok {
				up, down = humanRate(r[0]), humanRate(r[1])
			} else {
				up, down = humanRate(0), humanRate(0)
			}
		}
		rows = append(rows, []string{
			asciiSafe(a.Login),
			watchState(a.Active),
			fmt.Sprintf("%d", a.Connections),
			up, down,
			asciiSafe(strings.Join(watchNodeLabels(a), ", ")),
			asciiSafe(strings.Join(a.Countries, ", ")),
		})
	}
	writeASCIITable(w.out,
		[]string{"LOGIN", "STATE", "CONN", "UP/s", "DOWN/s", "NODES", "COUNTRIES"}, rows)
}

// frameJSON writes one JSON object per tick: rates are null on the first frame.
func (w *watchPrinter) frameJSON(st daemon.Status, rates map[string][2]float64, now time.Time) {
	accounts := watchSorted(st.Activity)
	type nodeJSON struct {
		Node          string `json:"node"`
		Name          string `json:"name,omitempty"`
		Provider      string `json:"provider,omitempty"`
		Country       string `json:"country,omitempty"`
		Connections   int    `json:"connections"`
		UploadBytes   int64  `json:"upload_bytes"`
		DownloadBytes int64  `json:"download_bytes"`
	}
	type accountJSON struct {
		Login       string     `json:"login"`
		Active      bool       `json:"active"`
		Connections int        `json:"connections"`
		UpBytesPS   *float64   `json:"up_bytes_per_s"`
		DownBytesPS *float64   `json:"down_bytes_per_s"`
		Nodes       []nodeJSON `json:"nodes"`
	}
	active := 0
	items := make([]accountJSON, 0, len(accounts))
	for _, a := range accounts {
		if a.Active {
			active++
		}
		item := accountJSON{Login: a.Login, Active: a.Active, Connections: a.Connections, Nodes: []nodeJSON{}}
		for _, n := range a.Nodes {
			item.Nodes = append(item.Nodes, nodeJSON{
				Node: n.Node, Name: n.Name, Provider: n.Provider, Country: n.Country,
				Connections: n.Connections, UploadBytes: n.UploadBytes, DownloadBytes: n.DownloadBytes,
			})
		}
		if rates != nil {
			if r, ok := rates[a.Login]; ok {
				item.UpBytesPS, item.DownBytesPS = &r[0], &r[1]
			} else {
				zero := 0.0
				item.UpBytesPS, item.DownBytesPS = &zero, &zero
			}
		}
		items = append(items, item)
	}
	doc := struct {
		Time           string        `json:"time"`
		ActiveAccounts int           `json:"active_accounts"`
		Accounts       []accountJSON `json:"accounts"`
	}{Time: now.UTC().Format(time.RFC3339), ActiveAccounts: active, Accounts: items}
	_ = json.NewEncoder(w.out).Encode(doc)
}

func (w *watchPrinter) warn(err error, now time.Time) {
	if w.jsonOut {
		doc := struct {
			Time    string `json:"time"`
			Warning string `json:"warning"`
		}{Time: now.UTC().Format(time.RFC3339), Warning: err.Error()}
		_ = json.NewEncoder(w.out).Encode(doc)
		return
	}
	fmt.Fprintf(w.out, "warning: poll failed: %v\n", err)
}

func (w *watchPrinter) stopped(now time.Time) {
	if w.jsonOut {
		doc := struct {
			Stopped bool   `json:"stopped"`
			Time    string `json:"time"`
		}{Stopped: true, Time: now.UTC().Format(time.RFC3339)}
		_ = json.NewEncoder(w.out).Encode(doc)
		return
	}
	w.beginFrame()
	fmt.Fprintln(w.out, "daemon stopped")
}

// watchSorted copies the activity sorted by login.
func watchSorted(activity []daemon.AccountActivity) []daemon.AccountActivity {
	out := append([]daemon.AccountActivity{}, activity...)
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out
}

func watchState(active bool) string {
	if active {
		return "active"
	}
	return "idle"
}

func watchUptime(startedAt string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return "0s"
	}
	d := now.Sub(t)
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Second).String()
}

// watchNodeLabels lists up to statusWatchMaxNodes nodes, busiest first, as
// the display name or the label, with "+N more" for the rest.
func watchNodeLabels(a daemon.AccountActivity) []string {
	nodes := append([]daemon.NodeUse{}, a.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Connections > nodes[j].Connections })
	labels := make([]string, 0, statusWatchMaxNodes+1)
	for i, n := range nodes {
		if i == statusWatchMaxNodes {
			labels = append(labels, fmt.Sprintf("+%d more", len(nodes)-statusWatchMaxNodes))
			break
		}
		label := n.Node
		if n.Name != "" {
			label = n.Name
		}
		labels = append(labels, label)
	}
	return labels
}

// humanRate renders bytes per second as B/s, KiB/s or MiB/s.
func humanRate(b float64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MiB/s", b/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KiB/s", b/1024)
	default:
		return fmt.Sprintf("%.0f B/s", b)
	}
}

// asciiSafe replaces every non-ASCII rune with '?', so the text table stays
// ASCII even when a node display name is not (NDJSON keeps the original).
func asciiSafe(s string) string {
	ascii := true
	for _, r := range s {
		if r > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r > 127 {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// writeASCIITable prints a header and rows padded to the widest cell.
func writeASCIITable(out io.Writer, header []string, rows [][]string) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	line := func(cells []string) {
		var b strings.Builder
		for i, cell := range cells {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", widths[i]-len(cell)))
		}
		fmt.Fprintln(out, strings.TrimRight(b.String(), " "))
	}
	line(header)
	for _, row := range rows {
		line(row)
	}
}

// runStatusWatch is the --watch entry point: it runs until interrupted.
func runStatusWatch(home string, interval time.Duration, jsonOut bool) error {
	ctx, cancel := statusWatchContext()
	defer cancel()
	poll := func() (daemon.Status, error) {
		// Derived from the watch context: Ctrl+C aborts an in-flight poll.
		pctx, pcancel := context.WithTimeout(ctx, daemon.ProbeTimeout)
		defer pcancel()
		_, st, err := daemonProbeLiveKeep(pctx, home)
		return st, err
	}
	return watchStatus(ctx, os.Stdout, poll, watchOptions{
		interval: interval,
		jsonOut:  jsonOut,
		term:     statusWatchTerminal(),
	})
}
