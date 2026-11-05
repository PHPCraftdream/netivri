package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

// The Providers screen nodes/diagnose features: the read-only Nodes dialog
// and the Diagnose flow, both through the shared operations (tui_nodes.go,
// ops_nodes.go, ops_diag.go).

// nodesHome seeds two providers ("p" with three stored nodes, "q" without a
// snapshot, "r" with an empty snapshot) and a loaded screen.
func nodesHome(t *testing.T) (string, *providersScreen) {
	t.Helper()
	h := providerSeed(t,
		config.Subscription{Name: "p", URL: "https://p.invalid"},
		config.Subscription{Name: "q", URL: "https://q.invalid"},
		config.Subscription{Name: "r", URL: "https://r.invalid"},
	)
	nodes := []subscription.Node{
		{ID: "aa11bb22cc33", Name: "Frankfurt 1", Type: "ss", Server: "srv.example.invalid", Port: 8388, Country: "DE"},
		{ID: "dd44ee55ff66", Name: "Tokyo 01", Type: "vmess", Server: "srv.example.invalid", Port: 443, Country: "JP"},
		{ID: "ab77cd88ef99", Name: "Helsinki 1", Type: "ss", Server: "srv.example.invalid", Port: 8388, Country: "FI"},
	}
	if err := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes, FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := subscription.NewStore(h+"/subscriptions").Save("r", subscription.Snapshot{FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	drainProvider(s, s.Init())
	return h, s
}

func selectProvider(s *providersScreen, name string) {
	s.list.SelectData(name)
	s.selected = name
	s.updateCard()
}

func openNodesDialog(t *testing.T, s *providersScreen) *nodesDialog {
	t.Helper()
	_, c := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	drainProvider(s, c)
	if s.nodesDlg == nil || s.mode != "nodes-dialog" {
		t.Fatalf("nodes dialog did not open: mode=%q dlg=%v", s.mode, s.nodesDlg)
	}
	return s.nodesDlg
}

// startDiagnoseRun walks g → node choice → samples and stops with the run
// command in hand (not executed): the screen sits in mode "diag-run".
func startDiagnoseRun(t *testing.T, s *providersScreen) tea.Cmd {
	t.Helper()
	_, c := s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	drainProvider(s, c)
	if s.mode != "diag-prompt-node" || s.prompt == nil {
		t.Fatalf("node prompt: mode=%q prompt=%v", s.mode, s.prompt)
	}
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.mode != "diag-prompt-samples" || s.prompt == nil {
		t.Fatalf("samples prompt: mode=%q prompt=%v", s.mode, s.prompt)
	}
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := c()
	_, run := s.Update(m)
	if s.mode != "diag-run" || s.diagCancel == nil {
		t.Fatalf("run state: mode=%q cancel=%v", s.mode, s.diagCancel)
	}
	return run
}

func TestProvidersNodesActionOpensDialog(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	d := openNodesDialog(t, s)
	if s.dialog != tui.Modal(d) {
		t.Fatal("the nodes dialog is not the active modal")
	}
	if len(d.list.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(d.list.Rows))
	}
	if first := d.list.Rows[0]; first.Cells[0] != "p/aa11bb22cc33" || first.Cells[1] != "DE" || first.Cells[2] != "Frankfurt 1" {
		t.Fatalf("row 0 = %v", first.Cells)
	}
	v := ansi.Strip(s.View(80, 24))
	for _, want := range []string{"Provider p: nodes", "p/aa11bb22cc33", "Frankfurt 1", "Tokyo 01", "c country: all", "Esc closes"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view misses %q:\n%s", want, v)
		}
	}
	// No address or port may appear, matching what the dialog is allowed to show.
	for _, banned := range []string{"srv.example.invalid", "8388"} {
		if strings.Contains(v, banned) {
			t.Fatalf("view leaks %q:\n%s", banned, v)
		}
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if esc == nil {
		t.Fatal("esc produced no close command")
	}
	drainProvider(s, esc)
	if s.dialog != nil || s.nodesDlg != nil || s.mode != "" {
		t.Fatal("esc did not close the nodes dialog")
	}
}

func TestProvidersNodesDialogFilterAndCountryCycle(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	d := openNodesDialog(t, s)
	s.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !d.list.Filtering {
		t.Fatal("/ did not start the filter")
	}
	providerType(s, "tokyo")
	if d.list.Filter != "tokyo" {
		t.Fatalf("filter = %q", d.list.Filter)
	}
	if row, ok := d.list.SelectedRow(); !ok || row.Cells[0] != "p/dd44ee55ff66" {
		t.Fatalf("selected row = %v", row.Cells)
	}
	s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.list.Filtering || d.list.Filter != "tokyo" {
		t.Fatalf("enter did not keep the filter: %v %q", d.list.Filtering, d.list.Filter)
	}
	if _, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); c != nil {
		t.Fatal("esc while filtering must not close the dialog")
	}
	if d.list.Filter != "" || d.list.Filtering {
		t.Fatal("esc did not clear the filter")
	}
	for cycle, want := range []string{"DE", "FI", "JP", "all"} {
		s.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
		if d.countryLabel() != want {
			t.Fatalf("cycle %d = %q, want %q", cycle+1, d.countryLabel(), want)
		}
		wantRows := 1
		if want == "all" {
			wantRows = 3
		}
		if len(d.list.Rows) != wantRows {
			t.Fatalf("cycle %q rows = %d, want %d", want, len(d.list.Rows), wantRows)
		}
		if want != "all" && d.list.Rows[0].Cells[1] != want {
			t.Fatalf("cycle %q row country = %q", want, d.list.Rows[0].Cells[1])
		}
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainProvider(s, esc)
	if s.dialog != nil {
		t.Fatal("esc did not close the dialog")
	}
}

func TestProvidersNodeDialogsFrameContract(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	openNodesDialog(t, s)
	for _, drilled := range []bool{false, true} {
		s.workspace.Drilled = drilled
		for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
			v := ansi.Strip(s.View(size[0], size[1]))
			lines := strings.Split(v, "\n")
			if len(lines) != size[1] {
				t.Fatalf("nodes dialog %dx%d: %d lines", size[0], size[1], len(lines))
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > size[0] {
					t.Fatalf("nodes dialog %dx%d: line wider than the screen: %q", size[0], size[1], l)
				}
			}
		}
	}
	for _, small := range [][2]int{{80, 1}, {80, 2}} {
		if lines := strings.Split(ansi.Strip(s.View(small[0], small[1])), "\n"); len(lines) != small[1] {
			t.Fatalf("nodes dialog %dx%d: %d lines", small[0], small[1], len(lines))
		}
	}
	d := core.NodeDiag{
		Label: "p/aa11bb22cc33", Samples: 5,
		DNS:       core.PhaseStats{N: 5, P50: 12 * time.Millisecond, P90: 15 * time.Millisecond, Max: 20 * time.Millisecond},
		TCP:       core.PhaseStats{N: 5, P50: 1500 * time.Microsecond, P90: 2500 * time.Microsecond, Max: 3 * time.Millisecond},
		FirstByte: core.PhaseStats{N: 4, P50: 10 * time.Millisecond, P90: 20 * time.Millisecond, Max: 30 * time.Millisecond},
		Failures:  map[string]int{"first_byte": 1},
	}
	if _, cmd := s.result(providerResultMsg{kind: "diag", value: d}); cmd != nil {
		t.Fatal("a diag result must not schedule work")
	}
	if s.diagDlg == nil || s.mode != "diag-result" {
		t.Fatalf("diag result dialog: mode=%q dlg=%v", s.mode, s.diagDlg)
	}
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		v := ansi.Strip(s.View(size[0], size[1]))
		lines := strings.Split(v, "\n")
		if len(lines) != size[1] {
			t.Fatalf("diag dialog %dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > size[0] {
				t.Fatalf("diag dialog %dx%d: line wider than the screen: %q", size[0], size[1], l)
			}
		}
	}
	v := ansi.Strip(s.View(80, 24))
	for _, want := range []string{"Diagnosis of p/aa11bb22cc33", "5 samples", "p50 12 ms", "p50 1.5 ms", "p90 2.5 ms", "max 3 ms", "p50 10 ms", "n 4", "Failures: dns 0  tcp 0  tls 0  first_byte 1", "Esc closes"} {
		if !strings.Contains(v, want) {
			t.Fatalf("diag view misses %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "tls  p50") {
		t.Fatalf("a phase without samples must have no line:\n%s", v)
	}
	for _, banned := range []string{"srv.example.invalid", "8388"} {
		if strings.Contains(v, banned) {
			t.Fatalf("diag view leaks %q", banned)
		}
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainProvider(s, esc)
	if s.dialog != nil || s.diagDlg != nil || s.mode != "" {
		t.Fatal("esc did not close the diagnose result")
	}
}

func TestProvidersDiagnoseFlowRunsSharedOperation(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	var gotLabel string
	var gotSamples int
	var dials int
	s.dialNode = func(ctx context.Context, home, label string, samples int) (core.NodeDiag, error) {
		dials++
		if ctx == nil {
			t.Error("the context is nil")
		}
		if home != s.home {
			t.Errorf("home = %q, want %q", home, s.home)
		}
		gotLabel, gotSamples = label, samples
		return core.NodeDiag{Label: label, Samples: samples, TCP: core.PhaseStats{N: samples, P50: time.Millisecond, P90: 2 * time.Millisecond, Max: 3 * time.Millisecond}}, nil
	}
	_, c := s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	drainProvider(s, c)
	if s.mode != "diag-prompt-node" || s.prompt == nil || s.prompt.Kind != tui.ChoicePrompt {
		t.Fatalf("node prompt: mode=%q prompt=%v", s.mode, s.prompt)
	}
	if len(s.prompt.Options) != 3 || s.prompt.Options[0].Value != "p/aa11bb22cc33" {
		t.Fatalf("options = %v", s.prompt.Options)
	}
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.mode != "diag-prompt-samples" || s.prompt == nil || s.prompt.Kind != tui.NumberPrompt {
		t.Fatalf("samples prompt: mode=%q prompt=%v", s.mode, s.prompt)
	}
	if s.prompt.Value != "5" {
		t.Fatalf("samples preset = %q, want 5", s.prompt.Value)
	}
	run := startDiagnoseRunTail(t, s)
	if got := strings.Join(s.card.Info, " | "); !strings.Contains(got, "Diagnosing p/aa11bb22cc33") {
		t.Fatalf("busy not visible in the card: %q", got)
	}
	drainProvider(s, run)
	if dials != 1 || gotLabel != "p/aa11bb22cc33" || gotSamples != 5 {
		t.Fatalf("dial args = %q/%d dials=%d", gotLabel, gotSamples, dials)
	}
	if s.mode != "diag-result" || s.diagDlg == nil || s.dialog == nil {
		t.Fatalf("result state: mode=%q dlg=%v", s.mode, s.diagDlg)
	}
	v := ansi.Strip(s.View(80, 24))
	if !strings.Contains(v, "Diagnosis of p/aa11bb22cc33") || !strings.Contains(v, "p50 1 ms") || !strings.Contains(v, "5 samples") {
		t.Fatalf("result view:\n%s", v)
	}
	if got := strings.Join(s.card.Info, " | "); strings.Contains(got, "Diagnosing") {
		t.Fatalf("stale busy line: %q", got)
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainProvider(s, esc)
	if s.dialog != nil || s.mode != "" {
		t.Fatal("esc did not close the result")
	}
	// Esc at the node prompt cancels the flow.
	_, c = s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	drainProvider(s, c)
	_, esc = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainProvider(s, esc)
	if s.mode != "" || s.prompt != nil || s.dialog != nil {
		t.Fatal("esc at the node prompt did not cancel")
	}
	// Esc at the samples prompt cancels too.
	_, c = s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	drainProvider(s, c)
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	_, esc = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainProvider(s, esc)
	if s.mode != "" || s.prompt != nil || s.dialog != nil {
		t.Fatal("esc at the samples prompt did not cancel")
	}
}

func TestProvidersDiagnoseSamplesValidation(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	var dials int
	var gotSamples int
	s.dialNode = func(_ context.Context, _ string, _ string, samples int) (core.NodeDiag, error) {
		dials++
		gotSamples = samples
		return core.NodeDiag{Label: "p/aa11bb22cc33", Samples: samples}, nil
	}
	_, c := s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	drainProvider(s, c)
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	// Empty input is refused by the number prompt itself.
	s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.mode != "diag-prompt-samples" || s.prompt == nil || s.prompt.Error == nil {
		t.Fatalf("empty samples: mode=%q prompt=%v", s.mode, s.prompt)
	}
	// Zero stays in the prompt with a message.
	providerType(s, "0")
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.mode != "diag-prompt-samples" || s.prompt == nil || s.prompt.Error == nil || !strings.Contains(s.prompt.Error.Error(), "at least 1") {
		t.Fatalf("samples 0: mode=%q err=%v", s.mode, s.prompt.Error)
	}
	// Too many stays in the prompt with a message.
	s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	providerType(s, "25")
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.prompt == nil || s.prompt.Error == nil || !strings.Contains(s.prompt.Error.Error(), "at most 20") {
		t.Fatalf("samples 25: err=%v", s.prompt.Error)
	}
	// A valid count runs exactly one diagnosis.
	s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	s.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	providerType(s, "7")
	run := startDiagnoseRunTail(t, s)
	drainProvider(s, run)
	if dials != 1 || gotSamples != 7 || s.mode != "diag-result" || s.diagDlg == nil {
		t.Fatalf("valid samples: dials=%d samples=%d mode=%q dlg=%v", dials, gotSamples, s.mode, s.diagDlg)
	}
}

// startDiagnoseRunTail enters 7 at an already-open samples prompt and returns
// the run command (not executed).
func startDiagnoseRunTail(t *testing.T, s *providersScreen) tea.Cmd {
	t.Helper()
	if s.mode != "diag-prompt-samples" {
		t.Fatalf("samples prompt: mode=%q", s.mode)
	}
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := c()
	_, run := s.Update(m)
	if s.mode != "diag-run" {
		t.Fatalf("run: mode=%q", s.mode)
	}
	return run
}

func TestProvidersDiagnoseCancelShowsMessageOnly(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	// The blocked dial returns only when the context is canceled.
	s.dialNode = func(ctx context.Context, _ string, _ string, _ int) (core.NodeDiag, error) {
		<-ctx.Done()
		return core.NodeDiag{}, ctx.Err()
	}
	run := startDiagnoseRun(t, s)
	// The run closes the prompts: Esc reaches the screen and cancels it.
	if s.dialog != nil || s.prompt != nil {
		t.Fatal("the run must close the prompts so Esc reaches the screen")
	}
	if _, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); esc != nil {
		t.Fatal("esc during the run must not return a command")
	}
	if !s.diagCanceled {
		t.Fatal("esc did not mark the run canceled")
	}
	drainProvider(s, run)
	if s.mode != "" || s.dialog != nil || s.diagDlg != nil {
		t.Fatalf("canceled run left state: mode=%q dialog=%v", s.mode, s.dialog)
	}
	if s.message != "Diagnosis canceled" || s.hint != "" {
		t.Fatalf("message/hint = %q/%q", s.message, s.hint)
	}
	// A canceled result must not schedule work.
	if _, cmd := s.result(providerResultMsg{kind: "diag", err: context.Canceled}); cmd != nil {
		t.Fatal("a canceled diag result must not schedule work")
	}
	// A successful result arriving after Esc reports the cancellation.
	run = startDiagnoseRun(t, s)
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_, _ = s.Update(providerResultMsg{kind: "diag", value: core.NodeDiag{Label: "p/aa11bb22cc33", Samples: 5}})
	if s.message != "Diagnosis canceled" || s.diagDlg != nil || s.mode != "" {
		t.Fatalf("flag path: message=%q dlg=%v mode=%q", s.message, s.diagDlg, s.mode)
	}
}

func TestProvidersDiagAndNodesErrorsShownByMessageOnly(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	s.dialNode = func(context.Context, string, string, int) (core.NodeDiag, error) {
		return core.NodeDiag{}, &cli.Error{Code: "DIAG_FAILED", Message: "the run could not proceed", Exit: cli.ExitNetwork}
	}
	run := startDiagnoseRun(t, s)
	drainProvider(s, run)
	if s.mode != "" || s.dialog != nil || s.diagDlg != nil {
		t.Fatalf("failed run left state: mode=%q dialog=%v", s.mode, s.dialog)
	}
	if s.message != "the run could not proceed" || s.hint != "" {
		t.Fatalf("message/hint = %q/%q", s.message, s.hint)
	}
	s.dialNode = func(context.Context, string, string, int) (core.NodeDiag, error) {
		return core.NodeDiag{}, errors.New("dial tcp 10.0.0.1:1: private-details")
	}
	run = startDiagnoseRun(t, s)
	drainProvider(s, run)
	if s.message != "Provider operation failed" || strings.Contains(s.pendingToast, "private-details") {
		t.Fatalf("plain error leaked: %q/%q", s.message, s.pendingToast)
	}
	s.nodesList = func(string, string, string) ([]NodeEntry, error) {
		return nil, &cli.Error{Code: "SUB_SNAPSHOT_INVALID", Message: `the stored snapshot of subscription "p" cannot be read`, Hint: "run `netivri sub update` to fetch it again", Exit: cli.ExitInternal}
	}
	_, c := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	drainProvider(s, c)
	if s.dialog != nil || s.mode != "" {
		t.Fatal("a failed nodes load must not open the dialog")
	}
	if s.message != `the stored snapshot of subscription "p" cannot be read` || s.hint != "run `netivri sub update` to fetch it again" {
		t.Fatalf("nodes error message/hint = %q/%q", s.message, s.hint)
	}
}

func TestProvidersNodeActionsNeedASnapshot(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "q")
	actionDisabled := func(id string) bool {
		for _, l := range s.card.Lines {
			if l.ID == id && l.Kind == tui.CardAction {
				return l.Disabled
			}
		}
		return false
	}
	if !actionDisabled("nodes") || !actionDisabled("diagnose") {
		t.Fatal("nodes/diagnose must be disabled without a stored snapshot")
	}
	for _, k := range []struct {
		code rune
		text string
	}{{'n', "n"}, {'g', "g"}} {
		if _, c := s.Update(tea.KeyPressMsg{Code: k.code, Text: k.text}); c != nil || s.mode != "" || s.busy != "" {
			t.Fatalf("%s without snapshot: cmd=%v mode=%q busy=%q", k.text, c, s.mode, s.busy)
		}
	}
	for _, l := range s.card.Lines {
		if l.ID == "nodes" && l.Kind == tui.CardReadOnly {
			if l.Disabled {
				t.Fatal("the read-only Nodes line must not be flagged disabled")
			}
			break
		}
	}
}

func TestProvidersNodesDialogEmptyAndDiagnoseWithoutNodes(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "r")
	_, c := s.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	drainProvider(s, c)
	d := s.nodesDlg
	if d == nil || len(d.list.Rows) != 0 || d.countries != nil {
		t.Fatalf("empty snapshot dialog: dlg=%v rows=%d", d, len(d.list.Rows))
	}
	v := ansi.Strip(s.View(80, 24))
	if !strings.Contains(v, "No stored nodes") || !strings.Contains(v, "Provider r: nodes") {
		t.Fatalf("empty view:\n%s", v)
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainProvider(s, esc)
	if s.dialog != nil {
		t.Fatal("esc did not close the empty dialog")
	}
	_, c = s.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	drainProvider(s, c)
	if s.mode != "" || s.dialog != nil || s.prompt != nil {
		t.Fatalf("diagnose with no nodes: mode=%q dialog=%v", s.mode, s.dialog)
	}
	if s.message != "No stored nodes for this provider" {
		t.Fatalf("message = %q", s.message)
	}
}

func TestProvidersNodeActionsFromCardLines(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	idx := func(id string) int {
		for i, l := range s.card.Lines {
			if l.ID == id && l.Kind == tui.CardAction {
				return i
			}
		}
		t.Fatalf("card has no %s action", id)
		return 0
	}
	s.card.Selected = idx("nodes")
	_, c := s.activateCard()
	if c == nil || s.mode != "nodes-load" {
		t.Fatalf("nodes action: mode=%q cmd=%v", s.mode, c)
	}
	drainProvider(s, c)
	if s.nodesDlg == nil {
		t.Fatal("card nodes action did not open the dialog")
	}
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainProvider(s, esc)
	s.card.Selected = idx("diagnose")
	s.dialNode = func(context.Context, string, string, int) (core.NodeDiag, error) {
		return core.NodeDiag{Label: "p/aa11bb22cc33"}, nil
	}
	_, c = s.activateCard()
	if c == nil || s.mode != "diag-load" {
		t.Fatalf("diagnose action: mode=%q cmd=%v", s.mode, c)
	}
	drainProvider(s, c)
	if s.mode != "diag-prompt-node" || s.prompt == nil {
		t.Fatalf("diagnose card action did not open the node prompt: mode=%q", s.mode)
	}
}

func TestProvidersParityRefsIncludeNodesAndDiagnose(t *testing.T) {
	_, s := nodesHome(t)
	selectProvider(s, "p")
	got := map[string]bool{}
	for _, r := range s.ParityRefs() {
		got[r] = true
	}
	for _, want := range []string{"providers/list", "providers/nodes", "providers/diagnose", "providers/remove"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, s.ParityRefs())
		}
	}
}
