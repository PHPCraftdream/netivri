package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
)

var errPoll = errors.New("poll timed out")

func TestParseWatchInterval(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"2s", 2 * time.Second, true},
		{"1s", time.Second, true},
		{"60s", 60 * time.Second, true},
		{"500ms", 0, false},
		{"61s", 0, false},
		{"soon", 0, false},
		{"", 0, false},
	} {
		got, err := parseWatchInterval(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("parseWatchInterval(%q) = %v, %v", tc.in, got, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("parseWatchInterval(%q) accepted", tc.in)
		}
	}
}

func TestStatusWatchUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--interval", "2s"},
		{"--interval="},
		{"--watch", "--full"},
		{"--watch", "--interval", "500ms"},
		{"--watch", "--interval", "61s"},
		{"--watch", "--interval", "soon"},
		{"--watch", "--interval="},
		{"--watch", "extra"},
	} {
		err := runStatus(append([]string{"--home", t.TempDir()}, args...), &bytes.Buffer{})
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
			t.Errorf("status %v: err = %#v, want USAGE", args, err)
		}
	}
}

// pollStep is one scripted answer of the watch's poll: the first step is
// the very first poll, every later one belongs to one tick. The LAST step
// must end the watch by itself: an ErrNotRunning-style stop, or the third
// consecutive failure (INTERNAL), so the scenario ends by the loop
// returning on its own and never by the test cancelling under its feet.
type pollStep struct {
	st  angel.Status
	err error
}

// watchDriveCollect runs watchStatus over the scripted polls. The poll stub
// announces that it started and then takes exactly one scripted step, and
// every send and receive is bounded, so the order of events is
// deterministic. After the last step is delivered the driver waits for the
// loop to return on its own; only a failing test cancels early.
func watchDriveCollect(t *testing.T, steps []pollStep, jsonOut, term bool) (string, error) {
	t.Helper()
	const opTimeout = 5 * time.Second
	if len(steps) < 1 {
		t.Fatal("driver needs at least one step")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tickCh := make(chan time.Time)
	cmds := make(chan pollStep)
	pollStarted := make(chan struct{})
	out := &bytes.Buffer{}
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	n := 0
	now := func() time.Time { n++; return base.Add(time.Duration(n-1) * time.Second) }
	poll := func() (angel.Status, error) {
		select {
		case pollStarted <- struct{}{}:
		case <-time.After(opTimeout):
			return angel.Status{}, errors.New("test driver: poll start was not collected")
		}
		select {
		case s := <-cmds:
			return s.st, s.err
		case <-time.After(opTimeout):
			return angel.Status{}, errors.New("test driver: poll step was not delivered")
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- watchStatus(ctx, out, poll, watchOptions{
			interval: time.Second, jsonOut: jsonOut, term: term, ticks: tickCh, now: now,
		})
	}()
	awaitPoll := func(what string) {
		t.Helper()
		select {
		case <-pollStarted:
		case err := <-done:
			t.Fatalf("%s: watchStatus returned early: %v", what, err)
		case <-time.After(opTimeout):
			t.Fatalf("%s: poll did not start", what)
		}
	}
	answer := func(what string, s pollStep) {
		t.Helper()
		select {
		case cmds <- s:
		case <-time.After(opTimeout):
			t.Fatalf("%s: poll never took its step", what)
		}
	}
	awaitPoll("initial poll")
	answer("initial poll", steps[0])
	for i := 1; i < len(steps); i++ {
		select {
		case tickCh <- base.Add(time.Duration(i-1) * time.Second):
		case err := <-done:
			t.Fatalf("tick %d: watchStatus returned early: %v", i, err)
		case <-time.After(opTimeout):
			t.Fatalf("tick %d was not taken", i)
		}
		awaitPoll("tick")
		answer("tick", steps[i])
	}
	// The last step ends the watch by itself: wait for the result instead
	// of cancelling (a cancel here could be seen before the last answer
	// is handled and drop frames or errors).
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(opTimeout):
		t.Fatal("the last scripted step did not end the watch; it must be terminal (a stop or the third failure)")
	}
	return out.String(), nil
}

// watchOK is a successful status with alice (active) and zoe (idle).
func watchOK(up, down int64) angel.Status {
	return angel.Status{
		Listen: "127.0.0.1:7890", StartedAt: "2026-10-25T11:59:00Z",
		Activity: []angel.AccountActivity{
			{Login: "zoe", Active: false, Connections: 0, Nodes: []angel.NodeUse{}},
			{Login: "alice", Active: true, Connections: 2, UploadBytes: up, DownloadBytes: down,
				Countries: []string{"DE", "NL"},
				Nodes: []angel.NodeUse{
					{Node: "main/a1", Name: "Germany 1", Provider: "main", Country: "DE", Connections: 2},
					{Node: "main/b2", Connections: 1},
					{Node: "main/c3", Connections: 0},
					{Node: "main/d4", Connections: 0},
				}},
		},
	}
}

// rowFor returns the table row of one login inside a text frame.
func rowFor(t *testing.T, frame, login string) string {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, login) {
			return line
		}
	}
	t.Fatalf("frame has no row for %s: %q", login, frame)
	return ""
}

// headerLine returns the table header line of a text frame.
func headerLine(t *testing.T, frame string) string {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "netivri status --watch") {
		t.Fatalf("frame has no header: %q", frame)
	}
	return lines[1]
}

// cells splits a fixed-width table row into its cells, using the header
// line for the column offsets (every cell is left-aligned and padded to
// the column width, with two separator spaces between columns).
func cells(t *testing.T, header, row string) []string {
	t.Helper()
	var starts []int
	inWord := false
	for i := 0; i < len(header); i++ {
		if header[i] != ' ' && !inWord {
			starts = append(starts, i)
			inWord = true
		} else if header[i] == ' ' {
			inWord = false
		}
	}
	out := make([]string, len(starts))
	for i, start := range starts {
		end := len(row)
		if i+1 < len(starts) {
			end = starts[i+1] - 2 // the two separator spaces
		}
		if start > len(row) {
			start = len(row)
		}
		if end > len(row) {
			end = len(row)
		}
		if start >= end {
			out[i] = ""
			continue
		}
		out[i] = strings.TrimRight(row[start:end], " ")
	}
	return out
}

func TestStatusWatchTextFrames(t *testing.T) {
	steps := []pollStep{
		{st: watchOK(0, 0)},
		{st: watchOK(4096, 1024)},
		{err: angel.ErrNotRunning}, // ends the watch; its final line is expected
	}
	out, err := watchDriveCollect(t, steps, false, false)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RUNNING" || ce.Exit != cli.ExitNoAngel {
		t.Fatalf("err = %#v", err)
	}
	frames := strings.Split(strings.Trim(out, "\n"), "\n\n")
	if len(frames) != 3 || frames[2] != "angel stopped" {
		t.Fatalf("got %d segments, last %q: %q", len(frames), frames[len(frames)-1], out)
	}
	if !strings.Contains(frames[0], "listen=127.0.0.1:7890") ||
		!strings.Contains(frames[0], "uptime=1m0s") ||
		!strings.Contains(frames[0], "active_accounts=1") {
		t.Errorf("header = %q", frames[0])
	}
	// first frame: the UP/s and DOWN/s cells of the row are whitespace only
	header0 := headerLine(t, frames[0])
	firstCells := cells(t, header0, rowFor(t, frames[0], "alice"))
	if firstCells[3] != "" || firstCells[4] != "" {
		t.Errorf("first-frame rate cells must be blank, got %q and %q", firstCells[3], firstCells[4])
	}
	header1 := headerLine(t, frames[1])
	secondCells := cells(t, header1, rowFor(t, frames[1], "alice"))
	if secondCells[3] != "4.0 KiB/s" || secondCells[4] != "1.0 KiB/s" {
		t.Errorf("second frame rate cells = %q and %q", secondCells[3], secondCells[4])
	}
	// each expectation on its own row
	if !strings.Contains(rowFor(t, frames[1], "alice"), "active") {
		t.Errorf("alice row must say active: %q", rowFor(t, frames[1], "alice"))
	}
	zoe := rowFor(t, frames[1], "zoe")
	if !strings.Contains(zoe, "idle") {
		t.Errorf("zoe row must say idle: %q", zoe)
	}
	if !strings.Contains(rowFor(t, frames[1], "alice"), "Germany 1, main/b2, main/c3, +1 more") {
		t.Errorf("node truncation: %q", rowFor(t, frames[1], "alice"))
	}
	if !strings.Contains(rowFor(t, frames[1], "alice"), "DE, NL") {
		t.Errorf("countries: %q", rowFor(t, frames[1], "alice"))
	}
	if strings.Index(out, "alice") > strings.Index(out, "zoe") {
		t.Errorf("accounts not sorted by login: %q", out)
	}
}

func TestStatusWatchNegativeDeltaShowsZero(t *testing.T) {
	// alice's counters go down (a restart or closed connections), carol's
	// go up: the zero must come from alice's own rate cells, not from
	// elsewhere in the output.
	withAlice := watchOK(4096, 1024)
	withAlice.Activity = append(withAlice.Activity, angel.AccountActivity{
		Login: "carol", Active: true, Connections: 1, Nodes: []angel.NodeUse{},
	})
	steps := []pollStep{
		{st: withAlice},
		{st: angel.Status{
			Listen: "127.0.0.1:7890", StartedAt: "2026-10-25T11:59:00Z",
			Activity: []angel.AccountActivity{
				{Login: "zoe", Active: false, Nodes: []angel.NodeUse{}},
				{Login: "alice", Active: true, Connections: 1,
					UploadBytes: 0, DownloadBytes: 0, Nodes: []angel.NodeUse{}},
				{Login: "carol", Active: true, Connections: 1,
					UploadBytes: 4096, DownloadBytes: 2048, Nodes: []angel.NodeUse{}},
			},
		}},
		{err: angel.ErrNotRunning},
	}
	out, err := watchDriveCollect(t, steps, false, false)
	if err == nil {
		t.Fatalf("err = nil, want the scripted stop")
	}
	frames := strings.Split(strings.Trim(out, "\n"), "\n\n")
	if len(frames) != 3 {
		t.Fatalf("got %d segments: %q", len(frames), out)
	}
	header := headerLine(t, frames[1])
	aliceCells := cells(t, header, rowFor(t, frames[1], "alice"))
	if aliceCells[3] != "0 B/s" || aliceCells[4] != "0 B/s" {
		t.Errorf("alice rate cells = %q and %q, want 0 B/s for both", aliceCells[3], aliceCells[4])
	}
	carolCells := cells(t, header, rowFor(t, frames[1], "carol"))
	if carolCells[3] != "4.0 KiB/s" || carolCells[4] != "2.0 KiB/s" {
		t.Errorf("carol rate cells = %q and %q", carolCells[3], carolCells[4])
	}
}

// jsonLines splits NDJSON output: the output must end with exactly one
// newline, and every line must be one non-empty JSON object with no ANSI
// escapes anywhere.
func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	if strings.Contains(out, "\x1b") {
		t.Errorf("JSON output contains an ANSI escape: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("JSON output does not end with a newline: %q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Errorf("JSON output has an extra trailing empty line: %q", out)
	}
	raw := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	lines := make([]map[string]any, 0, len(raw))
	for i, line := range raw {
		if strings.TrimSpace(line) == "" {
			t.Errorf("line %d is empty: %q", i, out)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("line %d is not one JSON object: %v (%q)", i, err, line)
			continue
		}
		lines = append(lines, m)
	}
	return lines
}

func TestStatusWatchNDJSON(t *testing.T) {
	steps := []pollStep{
		{st: watchOK(0, 0)},
		{st: watchOK(4096, 1024)},
		{err: angel.ErrNotRunning},
	}
	out, err := watchDriveCollect(t, steps, true, false)
	if err == nil {
		t.Fatal("err = nil, want the scripted stop")
	}
	lines := jsonLines(t, out)
	if len(lines) != 3 {
		t.Fatalf("got %d JSON lines: %q", len(lines), out)
	}
	first := lines[0]
	if first["time"] != "2026-10-25T12:00:00Z" || first["active_accounts"] != float64(1) {
		t.Errorf("first = %+v", first)
	}
	accs := first["accounts"].([]any)
	if len(accs) != 2 {
		t.Fatalf("first frame accounts = %+v", accs)
	}
	a0 := accs[0].(map[string]any)
	if a0["login"] != "alice" || a0["up_bytes_per_s"] != nil || a0["down_bytes_per_s"] != nil {
		t.Errorf("first frame rates must be null: %+v", a0)
	}
	second := lines[1]
	a := second["accounts"].([]any)[0].(map[string]any)
	if a["up_bytes_per_s"].(float64) != 4096 || a["down_bytes_per_s"].(float64) != 1024 {
		t.Errorf("second frame rates = %+v", a)
	}
	nodes := a["nodes"].([]any)
	if len(nodes) != 4 || nodes[0].(map[string]any)["name"] != "Germany 1" {
		t.Errorf("nodes = %+v", nodes)
	}
	if lines[2]["stopped"] != true || lines[2]["time"] == "" {
		t.Errorf("final object = %+v", lines[2])
	}
}

// TestStatusWatchJSONOneObjectPerLine drives a watch through frames, a
// warning and the angel-stopped ending on a terminal: every line must be
// exactly one JSON object, never blank, never an ANSI sequence.
func TestStatusWatchJSONOneObjectPerLine(t *testing.T) {
	steps := []pollStep{
		{st: watchOK(0, 0)},
		{err: errPoll},
		{st: watchOK(4096, 1024)},
		{err: angel.ErrNotRunning},
	}
	out, err := watchDriveCollect(t, steps, true, true)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RUNNING" {
		t.Fatalf("err = %#v", err)
	}
	lines := jsonLines(t, out)
	if len(lines) != 4 {
		t.Fatalf("got %d JSON lines: %q", len(lines), out)
	}
	if lines[1]["warning"] != errPoll.Error() {
		t.Errorf("warning line = %+v", lines[1])
	}
	if lines[3]["stopped"] != true || lines[3]["time"] == "" {
		t.Errorf("stopped line = %+v", lines[3])
	}
}

func TestStatusWatchAngelStops(t *testing.T) {
	steps := []pollStep{{st: watchOK(0, 0)}, {err: angel.ErrNotRunning}}
	out, err := watchDriveCollect(t, steps, false, false)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RUNNING" || ce.Exit != cli.ExitNoAngel {
		t.Fatalf("err = %#v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[len(lines)-1] != "angel stopped" {
		t.Errorf("missing final line: %q", out)
	}
}

func TestStatusWatchAngelStopsNDJSON(t *testing.T) {
	steps := []pollStep{{st: watchOK(0, 0)}, {err: angel.ErrNotRunning}}
	out, err := watchDriveCollect(t, steps, true, false)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RUNNING" || ce.Exit != cli.ExitNoAngel {
		t.Fatalf("err = %#v", err)
	}
	lines := jsonLines(t, out)
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), out)
	}
	last := lines[len(lines)-1]
	if last["stopped"] != true || last["time"] == "" {
		t.Errorf("final object = %+v", last)
	}
}

func TestStatusWatchNotRunningAtStart(t *testing.T) {
	steps := []pollStep{{err: angel.ErrNotRunning}}
	out, err := watchDriveCollect(t, steps, false, false)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RUNNING" || ce.Exit != cli.ExitNoAngel {
		t.Fatalf("err = %#v", err)
	}
	if out != "" {
		t.Errorf("output before any frame: %q", out)
	}
}

func TestStatusWatchRetriesThenInternal(t *testing.T) {
	// two failures in a row are only warnings: the watch goes on; the
	// scripted stop ends the scenario (the loop returns by itself)
	steps := []pollStep{
		{st: watchOK(0, 0)}, {err: errPoll}, {err: errPoll}, {err: angel.ErrNotRunning},
	}
	out, err := watchDriveCollect(t, steps, false, false)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RUNNING" {
		t.Fatalf("err = %#v", err)
	}
	if strings.Count(out, "warning: poll failed") != 2 {
		t.Errorf("warnings = %q", out)
	}
	// a third failure ends the watch with INTERNAL, by the loop itself;
	// its own step slice, so no state leaks between the scenarios
	steps = []pollStep{
		{st: watchOK(0, 0)}, {err: errPoll}, {err: errPoll}, {err: errPoll},
	}
	out, err = watchDriveCollect(t, steps, false, false)
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || ce.Exit != cli.ExitInternal {
		t.Fatalf("err = %#v", err)
	}
	if strings.Count(out, "warning: poll failed") != 3 {
		t.Errorf("warnings = %q", out)
	}
}

func TestStatusWatchRetriesOnFirstPollFailure(t *testing.T) {
	steps := []pollStep{
		{err: errPoll}, {err: errPoll}, {st: watchOK(0, 0)}, {err: angel.ErrNotRunning},
	}
	out, err := watchDriveCollect(t, steps, false, false)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RUNNING" {
		t.Fatalf("err = %#v", err)
	}
	if strings.Count(out, "warning: poll failed") != 2 {
		t.Errorf("warnings = %q", out)
	}
	if !strings.Contains(rowFor(t, out, "alice"), "active") {
		t.Errorf("the frame after the retries is missing: %q", out)
	}
}

func TestStatusWatchWarnIsNDJSONToo(t *testing.T) {
	steps := []pollStep{{st: watchOK(0, 0)}, {err: errPoll}, {err: angel.ErrNotRunning}}
	out, err := watchDriveCollect(t, steps, true, false)
	if err == nil {
		t.Fatal("err = nil, want the scripted stop")
	}
	lines := jsonLines(t, out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %q", len(lines), out)
	}
	if lines[1]["warning"] != errPoll.Error() || lines[1]["time"] == "" {
		t.Errorf("warning = %+v", lines[1])
	}
}

func TestStatusWatchTerminalClearsScreen(t *testing.T) {
	steps := []pollStep{
		{st: watchOK(0, 0)},
		{st: watchOK(4096, 1024)},
		{err: angel.ErrNotRunning},
	}
	out, err := watchDriveCollect(t, steps, false, true)
	if err == nil {
		t.Fatal("err = nil, want the scripted stop")
	}
	// one clear per frame boundary: the two frames plus the final
	// "angel stopped" frame
	want := 0
	for _, s := range steps {
		if s.err == nil {
			want++
		}
	}
	want++ // the stopped boundary
	if got := strings.Count(out, "\x1b[H\x1b[2J"); got != want {
		t.Errorf("clears = %d, want %d: %q", got, want, out)
	}
	if strings.Contains(out, "\n\n") {
		t.Errorf("terminal mode must not use a blank separator: %q", out)
	}
	// non-terminal: blank line between frames, no ANSI codes
	out, err = watchDriveCollect(t, steps, false, false)
	if err == nil {
		t.Fatal("err = nil, want the scripted stop")
	}
	if !strings.Contains(out, "\n\n") || strings.Contains(out, "\x1b[") {
		t.Errorf("non-terminal frames: %q", out)
	}
}

// TestStatusWatchCancelReturnsCleanly goes through the production wiring
// (runStatusWatch with the statusWatchContext seam replaced by a test
// context): the context is cancelled while the second poll is in flight,
// the poll returns ctx.Err(), and watchStatus must return nil with only
// the frames printed before the cancel, ending at a newline.
func TestStatusWatchCancelReturnsCleanly(t *testing.T) {
	watchCtx, watchCancel := context.WithCancel(context.Background())
	oldCtxSeam := statusWatchContext
	statusWatchContext = func() (context.Context, context.CancelFunc) { return watchCtx, watchCancel }
	t.Cleanup(func() { statusWatchContext = oldCtxSeam; watchCancel() })

	oldKeep := angelProbeLiveKeep
	calls := 0
	angelProbeLiveKeep = func(ctx context.Context, _ string) (angel.State, angel.Status, error) {
		calls++
		if calls == 1 {
			return angel.State{}, watchOK(0, 0), nil
		}
		// In flight until the cancellation reaches it.
		<-ctx.Done()
		return angel.State{}, angel.Status{}, ctx.Err()
	}
	t.Cleanup(func() { angelProbeLiveKeep = oldKeep })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	home := t.TempDir()
	// A 1ms interval puts the second poll in flight quickly, so the
	// cancellation below lands while a poll is blocked on the context.
	done := make(chan error, 1)
	go func() { done <- runStatusWatch(home, time.Millisecond, false) }()

	// The first frame is three complete lines: header, alice, zoe. Reading
	// all three proves the frame was fully printed before the cancel.
	br := bufio.NewReader(r)
	var frame []string
	for i := 0; i < 3; i++ {
		type readRes struct {
			line string
			err  error
		}
		ch := make(chan readRes, 1)
		go func() {
			line, err := br.ReadString('\n')
			ch <- readRes{line, err}
		}()
		select {
		case res := <-ch:
			if res.err != nil {
				t.Fatalf("read frame line %d: %v", i, res.err)
			}
			frame = append(frame, res.line)
		case <-time.After(5 * time.Second):
			t.Fatalf("frame line %d never arrived", i)
		}
	}
	watchCancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancellation must return nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runStatusWatch did not return after the cancellation")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(frame, "") + string(rest)
	if n := strings.Count(out, "netivri status --watch"); n != 1 {
		t.Errorf("got %d headers after cancellation, want exactly the one frame: %q", n, out)
	}
	if strings.Contains(out, "angel stopped") {
		t.Errorf("cancellation must not print the stopped line: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("cancellation left a partial line: %q", out)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			t.Errorf("blank line in the output: %q", out)
		}
	}
}

func TestStatusWatchTextIsASCII(t *testing.T) {
	st := watchOK(0, 0)
	st.Activity[1].Nodes[0].Name = "ドイツ 1"
	st.Activity[1].Nodes[1].Name = "\U0001F1E9\U0001F1EA Berlin"
	steps := []pollStep{{st: st}, {err: angel.ErrNotRunning}}
	out, err := watchDriveCollect(t, steps, false, false)
	if err == nil {
		t.Fatal("err = nil, want the scripted stop")
	}
	for _, r := range out {
		if r > 127 {
			t.Errorf("text output is not ASCII: %q contains %q", out, string(r))
			break
		}
	}
	alice := rowFor(t, out, "alice")
	if !strings.Contains(alice, "??? 1") || !strings.Contains(alice, "?? Berlin") {
		t.Errorf("non-ASCII names not rendered as '?': %q", alice)
	}
}

func TestStatusWatchNDJSONKeepsUnicode(t *testing.T) {
	st := watchOK(0, 0)
	st.Activity[1].Nodes[0].Name = "ドイツ 1"
	steps := []pollStep{{st: st}, {err: angel.ErrNotRunning}}
	out, err := watchDriveCollect(t, steps, true, false)
	if err == nil {
		t.Fatal("err = nil, want the scripted stop")
	}
	if !strings.Contains(out, "ドイツ 1") {
		t.Errorf("NDJSON must keep the original name: %q", out)
	}
	jsonLines(t, out)
}

func TestHumanRate(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0 B/s"},
		{512, "512 B/s"},
		{2048, "2.0 KiB/s"},
		{1024 * 1024, "1.0 MiB/s"},
	} {
		if got := humanRate(tc.in); got != tc.want {
			t.Errorf("humanRate(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWatchNodeLabels(t *testing.T) {
	a := angel.AccountActivity{Nodes: []angel.NodeUse{
		{Node: "main/a", Connections: 1},
		{Node: "main/b", Name: "B", Connections: 3},
		{Node: "main/c", Connections: 2},
		{Node: "main/d", Connections: 0},
	}}
	got := strings.Join(watchNodeLabels(a), ", ")
	if got != "B, main/c, main/a, +1 more" {
		t.Errorf("watchNodeLabels = %q", got)
	}
	if got := strings.Join(watchNodeLabels(angel.AccountActivity{}), ", "); got != "" {
		t.Errorf("no nodes = %q", got)
	}
}
