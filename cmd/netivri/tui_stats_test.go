package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

type statsFixture struct {
	s      *statsScreen
	home   string
	toasts []string
	reads  []StatsOptions
	base   func(string, StatsOptions) (StatsResult, error)
}

func newStatsFixture(t *testing.T, seed bool) *statsFixture {
	t.Helper()
	f := &statsFixture{home: t.TempDir()}
	if seed {
		seedStats(t, f.home)
	}
	env := tui.Env{
		Home:       f.home,
		Background: inlineBackground,
		Toast:      func(text string) tea.Cmd { f.toasts = append(f.toasts, text); return nil },
	}
	f.s = newStatsScreen(f.home, env).(*statsScreen)
	f.base = f.s.read
	f.s.read = func(home string, o StatsOptions) (StatsResult, error) {
		f.reads = append(f.reads, o)
		return f.base(home, o)
	}
	f.drain(f.s.Init())
	return f
}

func (f *statsFixture) drain(c tea.Cmd) {
	f.s = parityDrain(f.s, c).(*statsScreen)
}

func (f *statsFixture) msg(m tea.Msg) {
	sc, c := f.s.Update(m)
	f.s = sc.(*statsScreen)
	f.drain(c)
}

func (f *statsFixture) key(k string) {
	r := []rune(k)
	f.msg(tea.KeyPressMsg{Code: r[0], Text: k})
}

func (f *statsFixture) size(w, h int) { f.msg(tea.WindowSizeMsg{Width: w, Height: h}) }

// cardFocus focuses the card pane at a wide size, as the frame does on tab focus.
func (f *statsFixture) cardFocus() {
	f.size(100, 24)
	f.s.SetPaneFocus(tui.PaneCard)
}

// promptCancel leaves a changed prompt: esc asks about the typed text, tab
// moves to Discard, enter cancels.
func (f *statsFixture) promptCancel() {
	f.msg(tea.KeyPressMsg{Code: tea.KeyEscape})
	f.msg(tea.KeyPressMsg{Code: tea.KeyTab})
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestStatsScreenShowsSeededNodes(t *testing.T) {
	f := newStatsFixture(t, true)
	if len(f.s.list.Rows) != 2 {
		t.Fatalf("rows = %d", len(f.s.list.Rows))
	}
	if f.s.list.Rows[0].Data != "alt/b" || f.s.list.Rows[1].Data != "main/a" {
		t.Fatalf("rows = %v / %v", f.s.list.Rows[0].Data, f.s.list.Rows[1].Data)
	}
	c := f.s.workspace.Card
	if c.Title != "Node alt/b" {
		t.Fatalf("card title = %q", c.Title)
	}
	if l, _ := cardLine(c, "probes"); l.Value != "1" {
		t.Fatalf("alt/b probes = %+v", l)
	}
	if l, _ := cardLine(c, "uptime-pct"); l.Value != "100%" {
		t.Fatalf("alt/b uptime = %+v", l)
	}
	if l, _ := cardLine(c, "dial-ok-pct"); l.Value != "—" {
		t.Fatalf("alt/b dial ok = %+v", l)
	}
	f.key("j")
	c = f.s.workspace.Card
	if c.Title != "Node main/a" {
		t.Fatalf("card title = %q", c.Title)
	}
	for id, want := range map[string]string{
		"probes":         "4",
		"uptime-pct":     "75%",
		"delay-avg":      "200 ms",
		"delay-p50":      "200 ms",
		"delay-p95":      "300 ms",
		"dials":          "3",
		"dial-ok-pct":    "66.7%",
		"dial-avg":       "100 ms",
		"throughput-p50": "1024 KiB/s",
		"throughput-max": "1024 KiB/s",
		"bytes":          "1.0 MiB",
		"name":           "Germany 1",
	} {
		if l, _ := cardLine(c, id); l.Value != want {
			t.Errorf("%s = %q, want %q", id, l.Value, want)
		}
	}
	if got := strings.Join(f.s.list.Rows[1].Cells, "|"); !strings.Contains(got, "75%") || !strings.Contains(got, "200/300") {
		t.Fatalf("main/a row = %q", got)
	}
}

func TestStatsEmptyState(t *testing.T) {
	f := newStatsFixture(t, false)
	if len(f.s.list.Rows) != 0 {
		t.Fatalf("rows = %d", len(f.s.list.Rows))
	}
	if len(f.toasts) != 0 {
		t.Fatalf("toasts = %v: the missing database is not an error", f.toasts)
	}
	c := f.s.workspace.Card
	if c.Title != "Statistics" {
		t.Fatalf("card title = %q", c.Title)
	}
	if len(c.Info) < 2 || !strings.Contains(c.Info[0], "angel has run for a while") {
		t.Fatalf("info = %v", c.Info)
	}
	if !strings.Contains(c.Info[1], statsPath(f.home)) {
		t.Fatalf("info = %v", c.Info)
	}
	f.size(100, 24)
	if v := ansi.Strip(f.s.View(100, 24)); !strings.Contains(v, "No statistics yet") {
		t.Fatalf("view =\n%s", v)
	}
}

func TestStatsGroupingToggle(t *testing.T) {
	f := newStatsFixture(t, true)
	f.key("b")
	if f.s.by != "account" {
		t.Fatalf("by = %q", f.s.by)
	}
	if len(f.s.list.Rows) != 2 || f.s.list.Rows[0].Data != "alice" || f.s.list.Rows[1].Data != "bob" {
		t.Fatalf("rows = %d", len(f.s.list.Rows))
	}
	c := f.s.workspace.Card
	if c.Title != "Account alice" {
		t.Fatalf("card title = %q", c.Title)
	}
	if l, _ := cardLine(c, "bytes-up"); l.Value != "1.0 MiB" {
		t.Fatalf("bytes up = %+v", l)
	}
	if l, _ := cardLine(c, "bytes-down"); l.Value != "2.0 MiB" {
		t.Fatalf("bytes down = %+v", l)
	}
	if l, _ := cardLine(c, "dials"); l.Value != "3" {
		t.Fatalf("dials = %+v", l)
	}
	if l, _ := cardLine(c, "dial-ok-pct"); l.Value != "66.7%" {
		t.Fatalf("dial ok = %+v", l)
	}
	if l, _ := cardLine(c, "countries"); l.Value != "DE" {
		t.Fatalf("countries = %+v", l)
	}
	if l, _ := cardLine(c, "providers"); l.Value != "main" {
		t.Fatalf("providers = %+v", l)
	}
	f.key("b")
	if f.s.by != "node" || len(f.s.list.Rows) != 2 || f.s.list.Rows[0].Data != "alt/b" {
		t.Fatalf("by = %q rows = %d", f.s.by, len(f.s.list.Rows))
	}
}

func TestStatsSincePromptValidatesAndApplies(t *testing.T) {
	f := newStatsFixture(t, true)
	f.cardFocus()
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.prompt == nil || f.s.promptMode != "since" {
		t.Fatalf("prompt = %+v mode = %q", f.s.prompt, f.s.promptMode)
	}
	if l, _ := cardLine(f.s.workspace.Card, "since"); l.Value != "24h" {
		t.Fatalf("since line = %+v", l)
	}
	for _, k := range []string{"s", "o", "o", "n"} {
		f.key(k)
	}
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog == nil || f.s.prompt == nil || f.s.prompt.Error == nil {
		t.Fatalf("dialog = %v, error = %v", f.s.dialog, f.s.prompt.Error)
	}
	n := len(f.reads)
	f.promptCancel()
	if f.s.dialog != nil || f.s.prompt != nil {
		t.Fatalf("the dialog stayed open: %v", f.s.dialog)
	}
	if f.s.since != defaultStatsWindow || len(f.reads) != n {
		t.Fatalf("since = %v, reads = %d", f.s.since, len(f.reads))
	}
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.promptMode != "since" {
		t.Fatalf("mode = %q", f.s.promptMode)
	}
	for i := 0; i < 3; i++ {
		f.msg(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	f.key("2")
	f.key("h")
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog != nil || f.s.since != 2*time.Hour {
		t.Fatalf("dialog = %v, since = %v", f.s.dialog, f.s.since)
	}
	if n := len(f.reads); n == 0 || f.reads[n-1].Window != 2*time.Hour {
		t.Fatalf("reads = %+v", f.reads)
	}
	if l, _ := cardLine(f.s.workspace.Card, "since"); l.Value != "2h" {
		t.Fatalf("since line = %+v", l)
	}
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	for i := 0; i < 2; i++ {
		f.msg(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog != nil || f.s.since != defaultStatsWindow {
		t.Fatalf("dialog = %v, since = %v", f.s.dialog, f.s.since)
	}
}

func TestStatsFilterPromptsApplyAndClear(t *testing.T) {
	for _, tc := range []struct {
		field string
		down  int
		text  string
	}{
		{"sub", 1, "alt"},
		{"country", 2, "DE"},
		{"node", 3, "main/a"},
		{"account", 4, "alice"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newStatsFixture(t, true)
			f.cardFocus()
			for i := 0; i < tc.down; i++ {
				f.msg(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
			if f.s.promptMode != tc.field {
				t.Fatalf("mode = %q, want %q", f.s.promptMode, tc.field)
			}
			for _, r := range tc.text {
				f.key(string(r))
			}
			f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
			if f.s.dialog != nil {
				t.Fatalf("%s: dialog still open", tc.field)
			}
			opt := f.reads[len(f.reads)-1]
			got := map[string]string{"sub": opt.Sub, "country": opt.Country, "node": opt.Node, "account": opt.Account}[tc.field]
			if got != tc.text {
				t.Fatalf("%s filter = %q, want %q", tc.field, got, tc.text)
			}
			if l, _ := cardLine(f.s.workspace.Card, tc.field); l.Value != tc.text {
				t.Fatalf("%s line = %+v", tc.field, l)
			}
			if len(f.s.list.Rows) != 1 {
				t.Fatalf("%s: rows = %d, want the one match", tc.field, len(f.s.list.Rows))
			}
			f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
			if f.s.promptMode != tc.field {
				t.Fatalf("%s: reopen mode = %q", tc.field, f.s.promptMode)
			}
			for range tc.text {
				f.msg(tea.KeyPressMsg{Code: tea.KeyBackspace})
			}
			f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
			if f.s.dialog != nil {
				t.Fatalf("%s: dialog still open after clearing", tc.field)
			}
			opt = f.reads[len(f.reads)-1]
			got = map[string]string{"sub": opt.Sub, "country": opt.Country, "node": opt.Node, "account": opt.Account}[tc.field]
			if got != "" {
				t.Fatalf("%s filter = %q, want empty", tc.field, got)
			}
			if l, _ := cardLine(f.s.workspace.Card, tc.field); l.Value != "—" {
				t.Fatalf("%s line = %+v, want —", tc.field, l)
			}
			if len(f.s.list.Rows) != 2 {
				t.Fatalf("%s: rows = %d, want both rows again", tc.field, len(f.s.list.Rows))
			}
		})
	}
}

func TestStatsCountryPromptRejectsInvalid(t *testing.T) {
	f := newStatsFixture(t, true)
	f.cardFocus()
	f.msg(tea.KeyPressMsg{Code: tea.KeyDown})
	f.msg(tea.KeyPressMsg{Code: tea.KeyDown})
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.promptMode != "country" {
		t.Fatalf("mode = %q", f.s.promptMode)
	}
	for _, k := range []string{"g", "e", "r", "m", "a", "n", "y"} {
		f.key(k)
	}
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog == nil || f.s.prompt == nil || f.s.prompt.Error == nil {
		t.Fatalf("dialog = %v, error = %v", f.s.dialog, f.s.prompt.Error)
	}
	f.promptCancel()
	if f.s.dialog != nil || f.s.country != "" {
		t.Fatalf("dialog = %v, country = %q", f.s.dialog, f.s.country)
	}
	if len(f.toasts) != 0 {
		t.Fatalf("toasts = %v", f.toasts)
	}
}

func TestStatsReloadKey(t *testing.T) {
	f := newStatsFixture(t, true)
	n := len(f.reads)
	f.key("r")
	if len(f.reads) != n+1 || f.s.busy != "" {
		t.Fatalf("reads = %d busy = %q", len(f.reads), f.s.busy)
	}
}

func TestStatsGroupByCardAction(t *testing.T) {
	f := newStatsFixture(t, true)
	f.cardFocus()
	for i := 0; i < 5; i++ {
		f.msg(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.by != "account" {
		t.Fatalf("by = %q", f.s.by)
	}
	if len(f.s.list.Rows) != 2 || f.s.list.Rows[0].Data != "alice" {
		t.Fatalf("rows = %d", len(f.s.list.Rows))
	}
}

func TestStatsReadErrorsShownByMessageOnly(t *testing.T) {
	f := newStatsFixture(t, false)
	f.s.read = func(string, StatsOptions) (StatsResult, error) {
		return StatsResult{}, errors.New("dial tcp 10.0.0.1:1: secret-looking failure")
	}
	f.key("r")
	if got := f.toasts[len(f.toasts)-1]; got != "statistics operation failed" {
		t.Fatalf("toast = %q", got)
	}
	f.size(100, 24)
	v := ansi.Strip(f.s.View(100, 24))
	if strings.Contains(v, "secret-looking") || strings.Contains(v, "10.0.0.1") {
		t.Fatalf("a foreign error must not be echoed:\n%s", v)
	}
	c := f.s.workspace.Card
	if len(c.Info) == 0 || !strings.Contains(c.Info[0], "statistics operation failed") {
		t.Fatalf("info = %v", c.Info)
	}
	f.s.read = func(string, StatsOptions) (StatsResult, error) {
		return StatsResult{}, &cli.Error{Code: "INTERNAL", Message: "read statistics: boom", Hint: "check the log", Exit: cli.ExitInternal}
	}
	f.key("r")
	if got := f.toasts[len(f.toasts)-1]; got != "read statistics: boom (check the log)" {
		t.Fatalf("toast = %q", got)
	}
}

func TestStatsFrameContract(t *testing.T) {
	for _, seed := range []bool{true, false} {
		f := newStatsFixture(t, seed)
		for _, drilled := range []bool{false, true} {
			for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
				f.s.workspace.Drilled = drilled && size[0] < 100
				v := ansi.Strip(f.s.View(size[0], size[1]))
				lines := strings.Split(v, "\n")
				if len(lines) != size[1] {
					t.Fatalf("seed=%v drilled=%v %dx%d: %d lines", seed, drilled, size[0], size[1], len(lines))
				}
				for _, l := range lines {
					if ansi.StringWidth(l) > size[0] {
						t.Fatalf("seed=%v drilled=%v %dx%d: line wider than the screen: %q", seed, drilled, size[0], size[1], l)
					}
				}
			}
		}
	}
}

func TestStatsParityRefs(t *testing.T) {
	f := newStatsFixture(t, false)
	want := []string{"stats/list", "stats/since", "stats/sub", "stats/country", "stats/node", "stats/account", "stats/by", "stats/reload"}
	if got := f.s.ParityRefs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("refs = %v, want %v", got, want)
	}
}

func TestStatsPanesAndHints(t *testing.T) {
	f := newStatsFixture(t, true)
	if f.s.Title() != "Stats" {
		t.Fatalf("title = %q", f.s.Title())
	}
	if got := f.s.KeyHints(); !strings.Contains(got, "b group by") {
		t.Fatalf("hints = %q", got)
	}
	if len(f.s.Bindings()) == 0 {
		t.Fatal("no bindings")
	}
	if f.s.CapturingInput() {
		t.Fatal("capturing without a dialog")
	}
	if f.s.FocusPane(false) {
		t.Fatal("focus must not move below 100 columns")
	}
	f.size(100, 24)
	if !f.s.FocusPane(false) || f.s.workspace.Focus() != tui.PaneCard {
		t.Fatal("focus must move to the card")
	}
	if f.s.FocusPane(false) {
		t.Fatal("FocusPane must stop at the card")
	}
	if !f.s.FocusPane(true) || f.s.workspace.Focus() != tui.PaneList {
		t.Fatal("focus must move back to the list")
	}
	if f.s.FocusPane(true) {
		t.Fatal("FocusPane must stop at the list")
	}
	f.s.SetPaneFocus(tui.PaneCard)
	if f.s.workspace.Focus() != tui.PaneCard {
		t.Fatal("SetPaneFocus did not select the card")
	}
	f.s.workspace.Drilled = true
	f.size(80, 24)
	if got := f.s.Breadcrumb(); got != "Stats / alt/b" {
		t.Fatalf("breadcrumb = %q", got)
	}
	f.cardFocus()
	f.msg(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !f.s.CapturingInput() {
		t.Fatal("a prompt must capture input")
	}
	if got := f.s.KeyHints(); got != "Prompt: enter saves · esc cancels" {
		t.Fatalf("prompt hints = %q", got)
	}
}

func TestStatsDurationInput(t *testing.T) {
	for d, want := range map[time.Duration]string{
		24 * time.Hour:                           "24h",
		7 * 24 * time.Hour:                       "7d",
		90 * time.Minute:                         "90m",
		2 * time.Hour:                            "2h",
		time.Hour + 30*time.Minute + time.Second: "1h30m1s",
	} {
		if got := statsDurationInput(d); got != want {
			t.Errorf("statsDurationInput(%v) = %q, want %q", d, got, want)
		}
	}
}

type deferredStatsFixture struct {
	s      *statsScreen
	toasts []string
}

func newDeferredStatsFixture(t *testing.T) *deferredStatsFixture {
	t.Helper()
	f := &deferredStatsFixture{}
	env := tui.Env{
		Background: func(label string, work func() tea.Msg) tea.Cmd {
			return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: work()} }
		},
		Toast: func(text string) tea.Cmd {
			f.toasts = append(f.toasts, text)
			return nil
		},
	}
	f.s = newStatsScreen(t.TempDir(), env).(*statsScreen)
	return f
}

func (f *deferredStatsFixture) deliver(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a deferred stats command")
	}
	screen, _ := f.s.Update(cmd())
	f.s = screen.(*statsScreen)
}

func statsTestResult(by, key string) StatsResult {
	return StatsResult{By: by, Items: []map[string]any{{"login": key, "label": key}}}
}

func TestStatsReloadDiscardsSupersededGroupingResult(t *testing.T) {
	f := newDeferredStatsFixture(t)
	f.s.read = func(string, StatsOptions) (StatsResult, error) {
		return statsTestResult("node", "old/node"), nil
	}
	oldNode := f.s.reload()
	f.s.read = func(string, StatsOptions) (StatsResult, error) {
		return statsTestResult("account", "new-account"), nil
	}
	newAccount := f.s.toggleBy()

	f.deliver(t, newAccount)
	if f.s.by != "account" || len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "new-account" {
		t.Fatalf("new account result not applied: by=%q rows=%v", f.s.by, f.s.list.Rows)
	}
	f.deliver(t, oldNode)
	if f.s.by != "account" || len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "new-account" {
		t.Fatalf("superseded node result replaced account rows: by=%q rows=%v", f.s.by, f.s.list.Rows)
	}
	if f.s.workspace.Card.Title != "Account new-account" || f.s.busy != "" {
		t.Fatalf("card=%q busy=%q after stale result", f.s.workspace.Card.Title, f.s.busy)
	}
}

func TestStatsReloadDiscardsStaleFilterErrorAndSuccess(t *testing.T) {
	f := newDeferredStatsFixture(t)
	f.s.items = []map[string]any{{"label": "previous"}}
	f.s.errText = "previous error"
	f.s.loaded = true
	f.s.refresh()

	f.s.sub = "old"
	f.s.read = func(_ string, opts StatsOptions) (StatsResult, error) {
		if opts.Sub != "old" {
			t.Fatalf("old request options = %+v", opts)
		}
		return statsTestResult("node", "old-result"), nil
	}
	oldSuccess := f.s.reload()

	f.s.sub = "middle"
	f.s.read = func(_ string, opts StatsOptions) (StatsResult, error) {
		if opts.Sub != "middle" {
			t.Fatalf("middle request options = %+v", opts)
		}
		return StatsResult{}, errors.New("stale failure")
	}
	staleError := f.s.reload()

	f.s.read = func(_ string, opts StatsOptions) (StatsResult, error) {
		if opts.Sub != "new" {
			t.Fatalf("new request options = %+v", opts)
		}
		return statsTestResult("node", "new-result"), nil
	}
	f.s.openPrompt("sub")
	screen, newResult := f.s.applyPrompt(tui.PromptResultMsg{Value: "new"})
	f.s = screen.(*statsScreen)

	f.deliver(t, staleError)
	if f.s.busy != "Loading statistics" || f.s.errText != "previous error" || !f.s.loaded ||
		len(f.s.items) != 1 || statsString(f.s.items[0], "label") != "previous" || len(f.toasts) != 0 {
		t.Fatalf("stale error changed pending state: busy=%q error=%q items=%v toasts=%v", f.s.busy, f.s.errText, f.s.items, f.toasts)
	}

	f.deliver(t, newResult)
	if f.s.busy != "" || f.s.errText != "" || len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "new-result" {
		t.Fatalf("new filter result not applied: busy=%q error=%q rows=%v", f.s.busy, f.s.errText, f.s.list.Rows)
	}
	f.deliver(t, oldSuccess)
	if f.s.busy != "" || f.s.errText != "" || len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "new-result" {
		t.Fatalf("stale success replaced newest result: busy=%q error=%q rows=%v", f.s.busy, f.s.errText, f.s.list.Rows)
	}
	if len(f.toasts) != 0 {
		t.Fatalf("stale error produced toast: %v", f.toasts)
	}
}

func TestStatsReloadDiscardsOlderSameFilterResult(t *testing.T) {
	f := newDeferredStatsFixture(t)
	f.s.read = func(string, StatsOptions) (StatsResult, error) {
		return statsTestResult("node", "old-same-filter"), nil
	}
	old := f.s.reload()
	f.s.read = func(string, StatsOptions) (StatsResult, error) {
		return statsTestResult("node", "new-same-filter"), nil
	}
	newer := f.s.reload()

	f.deliver(t, newer)
	if len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "new-same-filter" {
		t.Fatalf("newest same-filter result not applied: rows=%v", f.s.list.Rows)
	}
	f.deliver(t, old)
	if len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "new-same-filter" || f.s.busy != "" {
		t.Fatalf("older same-filter result replaced newest: rows=%v busy=%q", f.s.list.Rows, f.s.busy)
	}
}

func TestStatsReadMsgZeroGenerationOnlyAcceptedOnPristineScreen(t *testing.T) {
	f := newDeferredStatsFixture(t)
	f.s.applyRead(statsReadMsg{res: statsTestResult("node", "direct-fixture")})
	if len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "direct-fixture" || !f.s.loaded {
		t.Fatalf("zero-generation fixture was not applied on pristine screen: rows=%v loaded=%v", f.s.list.Rows, f.s.loaded)
	}

	f.s.read = func(string, StatsOptions) (StatsResult, error) {
		return statsTestResult("node", "reload-result"), nil
	}
	cmd := f.s.reload()
	f.s.applyRead(statsReadMsg{res: statsTestResult("node", "stale-zero-generation")})
	if f.s.busy != "Loading statistics" || len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "direct-fixture" {
		t.Fatalf("zero-generation result changed active reload: busy=%q rows=%v", f.s.busy, f.s.list.Rows)
	}

	f.deliver(t, cmd)
	if f.s.busy != "" || len(f.s.list.Rows) != 1 || f.s.list.Rows[0].Data != "reload-result" {
		t.Fatalf("current reload result was not applied: busy=%q rows=%v", f.s.busy, f.s.list.Rows)
	}
}
