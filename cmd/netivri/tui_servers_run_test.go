package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
	"github.com/ktav-lang/golang/pure"
)

// serversDrain recursively executes commands the way the real program would:
// it runs cmd(), unwraps tea.BatchMsg (bubbletea v2: `type BatchMsg []Cmd`,
// verified in bubbletea commands.go), feeds every other message into
// s.Update and recurses on the returned command. Depth-bound (12) so a
// hypothetical self-rescheduling loop cannot hang the test; the servers
// screen never schedules time-based commands, so no tick guard is needed at
// the screen level.
func serversDrain(t *testing.T, s *serversScreen, c tea.Cmd) {
	t.Helper()
	serversDrainN(t, s, c, 0)
}

func serversDrainN(t *testing.T, s *serversScreen, c tea.Cmd, depth int) {
	t.Helper()
	for c != nil && depth < 12 {
		msg := c()
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, m := range batch {
				if m != nil {
					serversDrainN(t, s, m, depth+1)
				}
			}
			return
		}
		_, c = s.Update(msg)
		depth++
	}
}

// serversFrameDrain is the frame-level counterpart. The frame wraps every
// background job in tea.Batch(work, spinnerTick) and toasts return a
// 3-second tea.Tick, so those two time-based branches are cut after their
// first message: spinner.TickMsg (charm.land/bubbles/v2/spinner) and
// tui.ToastMsg never carry work the servers screen needs.
func serversFrameDrain(t *testing.T, f *tui.Frame, c tea.Cmd, depth int) {
	t.Helper()
	if c == nil || depth > 8 {
		return
	}
	msg := c()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, m := range batch {
			if m != nil {
				serversFrameDrain(t, f, m, depth+1)
			}
		}
		return
	}
	_, next := f.Update(msg)
	switch msg.(type) {
	case spinner.TickMsg, tui.ToastMsg:
		return
	}
	serversFrameDrain(t, f, next, depth+1)
}

// serversFrameStep feeds one message into the frame and drains the command
// it returns.
func serversFrameStep(t *testing.T, f *tui.Frame, msg tea.Msg) {
	t.Helper()
	_, c := f.Update(msg)
	serversFrameDrain(t, f, c, 0)
}

func serversFrameKey(t *testing.T, f *tui.Frame, code rune, text string) {
	t.Helper()
	serversFrameStep(t, f, serversKey(code, text))
}

// serversTestFrame builds a real tui.NewFrame whose only tab is the servers
// screen for the given home, sized and initialized. The angel probe is
// stubbed to "not running" (status source errors => "angel stopped").
func serversTestFrame(t *testing.T, home string, width, height int) *tui.Frame {
	t.Helper()
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	f := tui.NewFrame(tui.Options{
		Home: home,
		Tabs: []tui.Tab{{Title: "Servers", New: func(e tui.Env) tui.Screen { return newServersScreen(home, e) }}},
	})
	_, _ = f.Update(tea.WindowSizeMsg{Width: width, Height: height})
	serversFrameDrain(t, f, f.Init(), 0)
	return f
}

func serversFrameView(t *testing.T, f *tui.Frame) string {
	t.Helper()
	return ansi.Strip(f.View().Content)
}

// serversFrameLoadDetail loads the detail card for the first list row via
// real frame keys: Enter on the list pane (frame routes it to the screen,
// screen "enter" case width>=100 + list focus => detailCmdForSelected),
// then Tab focuses the card pane (frame "tab" -> PaneFocusScreen.FocusPane).
func serversFrameLoadDetail(t *testing.T, f *tui.Frame) {
	t.Helper()
	serversFrameKey(t, f, tea.KeyEnter, "")
	serversFrameKey(t, f, tea.KeyTab, "")
}

// ---------------------------------------------------------------------------
// D: edit/rotate/remove flows drain background jobs (detail reload) so the
// card reflects saved values — the old serversRun dropped tea.BatchMsg
// silently (serversRunOne fed the BatchMsg straight into s.Update).
// ---------------------------------------------------------------------------

func serversLoadDetailDrained(t *testing.T, s *serversScreen) {
	t.Helper()
	if s.workspace != nil {
		s.workspace.SetSize(180, 30)
		s.workspace.SetFocus(tui.PaneList)
	}
	_, cmd := s.Update(serversKey(tea.KeyEnter, ""))
	if cmd == nil {
		t.Fatal("enter did not schedule detail")
	}
	serversDrain(t, s, cmd)
}

func TestServersEditPortDrainsReloadAndCardShowsNewValue(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversDrain(t, s, s.Init())
	serversLoadDetailDrained(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	_ = s.View(120, 30)
	if cmd := s.cardAction("port"); cmd != nil || s.dialog == nil || s.dialog.prompt == nil {
		t.Fatal("port edit dialog did not open")
	}
	serversBackspaces(s, 4)
	serversTypeKeys(s, "4599")
	if s.dialog.prompt.Value != "4599" {
		t.Fatalf("prompt value = %q", s.dialog.prompt.Value)
	}
	c := serversEnterKey(s) // accept() validates then result(false)
	if c == nil {
		t.Fatal("port save produced no command")
	}
	msg := c()
	res, ok := msg.(serversDialogResult)
	if !ok || res.Canceled || res.Values["value"] != "4599" {
		t.Fatalf("port save result = %#v", msg)
	}
	_, c = s.Update(msg)
	if c == nil {
		t.Fatal("dialog result did not start the update job")
	}
	serversDrain(t, s, c)
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cfg.GetServer("ss")
	if entry == nil || entry.Port != 4599 {
		t.Fatalf("port edit did not persist: %#v", entry)
	}
	// Hand-traced reload path: serversUpdateDone returns
	// tea.Batch(detailCmd(name), loadCmd, notice); the drained detailCmd
	// populates s.detail, whose card line "Port" is rendered by card.go.
	view := s.View(120, 30)
	if !strings.Contains(view, "4599") {
		t.Fatalf("card does not show the new port: %q", view)
	}
	for _, l := range s.workspace.Card.Lines {
		if l.ID == "port" && l.Value != "4599" {
			t.Fatalf("card port line = %q", l.Value)
		}
	}
}

// ---------------------------------------------------------------------------
// F(2): multi-account link selection (ChoicePrompt account list, then the
// credential warning, then the reveal).
// ---------------------------------------------------------------------------

// serversMultiAccountHome builds a home with accounts alice+bob and one
// mieru server "mm" serving both, with materialized secrets.
func serversMultiAccountHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	c := config.Default()
	c.Accounts = []config.Account{
		{Login: "alice", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"},
		{Login: "bob", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"},
	}
	c.Servers = []config.Server{{Name: "mm", Protocol: "mieru", Bind: "127.0.0.1", Port: 3400, PublicHost: "pub.test", Accounts: "alice,bob"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	if err := config.Save(config.ConfigPath(h), c); err != nil {
		t.Fatal(err)
	}
	specs, err := c.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if _, err := servers.NewStore(h).EnsureSecrets(spec); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestServersLinkMultiAccountChooseSecondAccount(t *testing.T) {
	home := serversMultiAccountHome(t)
	s := newServersTestScreen(t, home)
	serversDrain(t, s, s.Init())
	serversLoadDetailDrained(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	_ = s.View(120, 30)
	if len(s.detailAccounts) != 2 || s.detailAccounts[0] != "alice" || s.detailAccounts[1] != "bob" {
		t.Fatalf("detail accounts = %v, want [alice bob] (serverConfigFacts splits Accounts \"alice,bob\")", s.detailAccounts)
	}
	// 'l' with two accounts opens the account ChoicePrompt (showLinkChoice).
	if _, c := s.Update(serversKey('l', "l")); c != nil {
		t.Fatal("link hotkey produced a command")
	}
	d := s.dialog
	if d == nil || d.prompt == nil || d.prompt.Kind != tui.ChoicePrompt {
		t.Fatalf("account choice did not open: %+v", d)
	}
	if len(d.prompt.Options) != 2 || d.prompt.Options[0].Value != "alice" || d.prompt.Options[1].Value != "bob" {
		t.Fatalf("account options = %+v", d.prompt.Options)
	}
	_ = s.View(120, 30)
	serversDownKeys(s, 1) // moveChoice(1): choice=1 -> bob (prompt.go)
	c := serversEnterKey(s)
	if c == nil {
		t.Fatal("account pick produced no command")
	}
	serversDrain(t, s, c)
	// onResult -> confirmServerLink(mm, bob): credential warning prompt.
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.ConfirmPrompt || s.dialog.prompt.ConfirmLabel != "Show" {
		t.Fatalf("credential warning did not open after account pick: %+v", s.dialog)
	}
	// ConfirmPrompt 'y' -> confirmButton(1) -> result(false) -> onResult.
	c = nil
	if _, c = s.Update(serversKey('y', "y")); c == nil {
		t.Fatal("show confirm produced no command")
	}
	serversDrain(t, s, c)
	// confirmServerLink -> ServerLink job -> serversLinkDone -> reveal modal.
	if s.dialog == nil || s.dialog.modal == nil {
		t.Fatal("link result did not open the reveal modal")
	}
	v := s.View(120, 30)
	if !strings.Contains(v, "Client link · mm") || !strings.Contains(v, "Close") {
		t.Fatalf("reveal frame wrong: %q", v)
	}
}

// ---------------------------------------------------------------------------
// F(3): operation failures keep the dialog open with answers and an error.
// ---------------------------------------------------------------------------

func TestServersUpdatePortConflictKeepsPromptAndAnswer(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversDrain(t, s, s.Init())
	serversLoadDetailDrained(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	_ = s.View(120, 30)
	// Edit the ss port to 2345, used by "vless" on the same bind 127.0.0.1.
	// The port field prompt has no conflict validation (openServerFieldEdit:
	// NumberPrompt with Min/Max only), so the conflict is detected by
	// ServerUpdate -> serverUpdateConfig validation and reported through
	// serversUpdateDone{err} -> prompt.SetError + SetSaving(false).
	if cmd := s.cardAction("port"); cmd != nil || s.dialog == nil || s.dialog.prompt == nil {
		t.Fatal("port edit dialog did not open")
	}
	serversBackspaces(s, 4)
	serversTypeKeys(s, "2345")
	c := serversEnterKey(s)
	if c == nil {
		t.Fatal("save produced no command")
	}
	msg := c()
	res, ok := msg.(serversDialogResult)
	if !ok || res.Canceled {
		t.Fatalf("save result = %#v", msg)
	}
	_, c = s.Update(msg)
	serversDrain(t, s, c)
	d := s.dialog
	if d == nil || d.prompt == nil {
		t.Fatal("failed update closed the prompt")
	}
	if d.prompt.Error == nil {
		t.Fatal("conflict error not surfaced on the prompt")
	}
	if d.prompt.Value != "2345" {
		t.Fatalf("typed answer discarded: %q", d.prompt.Value)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cfg.GetServer("ss")
	if entry == nil || entry.Port != 1234 {
		t.Fatalf("conflicting port was persisted: %#v", entry)
	}
}

func TestServersAddDuplicateNameKeepsWizardOpenWithAnswers(t *testing.T) {
	home := serversFixture(t) // already has server "ss"
	s := newServersTestScreen(t, home)
	serversDrain(t, s, s.Init())
	_, _ = s.Update(serversKey('a', "a"))
	// Mieru walk: protocol -> rebuild (Index 1) -> Name "ss" (duplicate).
	serversWizardProtocol(t, s, "mieru")
	serversTypeKeys(s, "ss")
	if serversEnterKey(s) != nil {
		t.Fatal("name advance produced a command")
	}
	serversEnterKey(s) // Bind
	serversEnterKey(s) // Port
	serversEnterKey(s) // Public host
	serversEnterKey(s) // Transport
	serversEnterKey(s) // Multiplexing
	serversEnterKey(s) // Accounts -> review
	d := s.dialog
	if d == nil || !d.wizard.Review {
		t.Fatal("did not reach review")
	}
	c := serversEnterKey(s)
	if c == nil {
		t.Fatal("review create returned no command")
	}
	msg := c()
	res, ok := msg.(serversDialogResult)
	if !ok || res.Canceled {
		t.Fatalf("review result = %#v", msg)
	}
	_, c = s.Update(msg)
	if c == nil {
		t.Fatal("dialog result did not start creation")
	}
	// ServerAdd fails on the duplicate name ("server name is already
	// configured", ops_server.go:128) -> serversAddDone{err} ->
	// wizard.SetSaving(false) + SetError, wizard stays open.
	serversDrain(t, s, c)
	d = s.dialog
	if d == nil || d.wizard == nil {
		t.Fatal("failed add closed the wizard")
	}
	if d.wizard.Saving {
		t.Fatal("wizard left in saving state after failure")
	}
	if d.wizard.Error == nil {
		t.Fatal("add failure error not shown on the wizard")
	}
	if got := d.wizard.Steps[1].Prompt.Value; got != "ss" {
		t.Fatalf("typed name discarded: %q", got)
	}
}

// ---------------------------------------------------------------------------
// F(4): rotate really changes the stored credentials (real frame flow).
// ---------------------------------------------------------------------------

func serversStoredSecrets(t *testing.T, home string) map[string]map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "server-secrets.ktav"))
	if err != nil {
		t.Fatalf("secrets store: %v", err)
	}
	var doc struct {
		Servers map[string]struct {
			Material map[string]string            `json:"material"`
			Users    map[string]map[string]string `json:"users"`
		} `json:"servers"`
	}
	if err := pure.LoadsInto(string(b), &doc); err != nil {
		t.Fatalf("secrets store parse: %v", err)
	}
	out := map[string]map[string]string{}
	for name, e := range doc.Servers {
		all := map[string]string{}
		for k, v := range e.Material {
			all[k] = v
		}
		for user, fields := range e.Users {
			for k, v := range fields {
				all[user+"/"+k] = v
			}
		}
		out[name] = all
	}
	return out
}

func TestServersRotateChangesStoredSecretsThroughRealFrame(t *testing.T) {
	home := serversFixture(t)
	before := serversStoredSecrets(t, home)
	if len(before["ss"]) == 0 {
		t.Fatal("fixture has no ss secrets to rotate")
	}
	f := serversTestFrame(t, home, 120, 40)
	serversFrameLoadDetail(t, f)

	// 't' opens the rotate confirm through env.OpenModal; the returned cmd
	// produces modalOpen{m, index} (internal/tui/frame.go envFor) which the
	// frame turns into f.modal. No public modal accessor exists, so all
	// assertions are on the rendered frame and the on-disk effects.
	serversFrameKey(t, f, 't', "t")
	view := serversFrameView(t, f)
	if !strings.Contains(view, "Rotate keys for server ss") || !strings.Contains(view, "alice") {
		t.Fatalf("rotate confirm prompt wrong: %q", view)
	}
	// Default Cancel: confirm.go selected==0, Enter -> ConfirmResultMsg{Canceled}.
	serversFrameKey(t, f, tea.KeyEnter, "")
	afterCancel := serversStoredSecrets(t, home)
	for k, v := range before {
		if afterCancel[k] == nil {
			t.Fatalf("canceled rotate removed secrets for %s", k)
		}
		for kk, vv := range v {
			if afterCancel[k][kk] != vv {
				t.Fatalf("canceled rotate changed %s/%s", k, kk)
			}
		}
	}
	if strings.Contains(serversFrameView(t, f), "Rotate keys for server ss") {
		t.Fatal("confirm modal stayed open after Enter-cancel")
	}

	// Reopen and confirm with y.
	serversFrameKey(t, f, 't', "t")
	if !strings.Contains(serversFrameView(t, f), "Rotate keys for server ss") {
		t.Fatal("rotate confirm did not reopen")
	}
	serversFrameKey(t, f, 'y', "y")
	after := serversStoredSecrets(t, home)
	if len(after["ss"]) == 0 {
		t.Fatal("rotate removed ss secrets entirely")
	}
	changed := false
	for k, v := range before["ss"] {
		if !strings.HasPrefix(k, "alice/") {
			continue
		}
		if after["ss"][k] != v {
			changed = true
		}
		if after["ss"][k] != "" && strings.Contains(serversFrameView(t, f), after["ss"][k]) {
			t.Fatalf("rotated secret value rendered in the frame")
		}
	}
	if !changed {
		t.Fatal("stored account credential did not change across rotate")
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.GetServer("ss"); i < 0 {
		t.Fatal("rotate removed the config entry")
	}
	if !strings.Contains(serversFrameView(t, f), "Keys rotated for ss") {
		t.Fatal("rotate toast missing from the frame")
	}
}

// ---------------------------------------------------------------------------
// F(5): leaving the port empty auto-allocates a free port (serverPickPort in
// the create job) and the result modal shows the same number.
// ---------------------------------------------------------------------------

func TestServersAddEmptyPortAutoAllocates(t *testing.T) {
	home := serversWizardHome(t)
	s := newServersTestScreen(t, home)
	serversDrain(t, s, s.Init())
	_, _ = s.Update(serversKey('a', "a"))
	serversWizardProtocol(t, s, "mieru")
	serversTypeKeys(s, "autoport")
	serversEnterKey(s) // Name -> Bind
	serversEnterKey(s) // Bind -> Port (left empty)
	serversEnterKey(s) // Port -> Public host
	serversEnterKey(s) // Public host -> Transport
	serversEnterKey(s) // Transport -> Multiplexing
	serversEnterKey(s) // Multiplexing -> Accounts (alice,bob preselected)
	serversEnterKey(s) // Accounts -> review
	d := s.dialog
	if d == nil || !d.wizard.Review {
		t.Fatal("did not reach review")
	}
	c := serversEnterKey(s)
	if c == nil {
		t.Fatal("review create returned no command")
	}
	msg := c()
	_, c = s.Update(msg)
	if c == nil {
		t.Fatal("dialog result did not start creation")
	}
	// Port == 0 -> create job calls serverPickPort(bind) before ServerAdd
	// (tui_servers_wizard.go beginAddWizard onResult).
	serversDrain(t, s, c)
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cfg.GetServer("autoport")
	if entry == nil {
		t.Fatal("auto-ported server missing from config")
	}
	if entry.Port < 1 || entry.Port > 65535 {
		t.Fatalf("allocated port out of range: %d", entry.Port)
	}
	if s.dialog == nil || s.dialog.modal == nil {
		t.Fatal("result modal did not open")
	}
	v := s.View(120, 30)
	if !strings.Contains(v, "Port: "+strconv.Itoa(entry.Port)) {
		t.Fatalf("result modal port %d not shown: %q", entry.Port, v)
	}
	// Unit behavior of serverPickPort: a real local bind on loopback.
	p, err := serverPickPort("127.0.0.1")
	if err != nil || p < 1 || p > 65535 {
		t.Fatalf("serverPickPort = %d, %v", p, err)
	}
}

// ---------------------------------------------------------------------------
// F(6): the wizard port step validates conflicts against configured servers.
// ---------------------------------------------------------------------------

func TestServersWizardPortStepRejectsConflictingPort(t *testing.T) {
	home := serversFixture(t) // ss on 127.0.0.1:1234, vless on 127.0.0.1:2345
	s := newServersTestScreen(t, home)
	serversDrain(t, s, s.Init())
	_, _ = s.Update(serversKey('a', "a"))
	serversWizardProtocol(t, s, "mieru")
	serversTypeKeys(s, "clash") // Name
	serversEnterKey(s)          // -> Bind (0.0.0.0 default)
	serversEnterKey(s)          // -> Port
	serversTypeKeys(s, "1234")
	d := s.dialog
	if d.wizard.Index != 3 {
		t.Fatalf("port step index = %d", d.wizard.Index)
	}
	// Enter -> activate(1) -> saveCurrent(true) -> port.Validate ->
	// serverPortConflicts -> checkPortConflict: 1234/0.0.0.0 overlaps
	// ss 1234/127.0.0.1 and no share mode applies -> conflict.
	serversEnterKey(s)
	d = s.dialog
	if d == nil || d.wizard == nil || d.wizard.Index != 3 {
		t.Fatal("conflicting port advanced the wizard")
	}
	if d.wizard.Error == nil || !strings.Contains(d.wizard.Error.Error(), "conflicts") {
		t.Fatalf("conflict error missing: %v", d.wizard.Error)
	}
	if got := d.wizard.Steps[3].Prompt.Value; got != "1234" {
		t.Fatalf("typed port discarded: %q", got)
	}
}

// ---------------------------------------------------------------------------
// F(7): serverAutoGenSteps builds one step per AutoGenerated parameter.
// ---------------------------------------------------------------------------

func TestServersAutoGenStepsKeysGenPrefix(t *testing.T) {
	proto := ServerProtocol{Parameters: []ServerParameter{{AutoGenerated: true, Label: "K", Name: "k"}, {Label: "Plain", Name: "plain"}}}
	steps := serverAutoGenSteps(proto)
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1 (only AutoGenerated params)", len(steps))
	}
	if steps[0].Key != "gen:k" {
		t.Fatalf("step key = %q, want gen:k", steps[0].Key)
	}
	if steps[0].Prompt == nil || !strings.Contains(steps[0].Prompt.TitleText, "K") {
		t.Fatalf("gen prompt wrong: %+v", steps[0].Prompt)
	}
}

// ---------------------------------------------------------------------------
// G: golden texts. Every line is hand-derived from the render code:
// workspace layout.go (35% split at width>=100, joined as
// listLine + " " + cardLine), list.go (columnWidths, renderCells, FocusMark
// "›", geometry, fmtPosition), card.go placeholder card, and the
// "angel stopped" state (the stubbed status probe errors). Rows are
// compared ANSI-stripped; trailing spaces are insignificant because
// serversBound/list border padding is regenerated by the helpers below.
// ---------------------------------------------------------------------------

func serversGoldenHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	c := config.Default()
	c.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"}}
	c.Servers = []config.Server{
		{Name: "ss", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 1234, Accounts: "alice", Params: map[string]string{"cipher": "aes-128-gcm"}},
		{Name: "vl", Protocol: "vless-reality", Bind: "127.0.0.1", Port: 2345, Accounts: "alice", Params: map[string]string{"sni": "golden.example"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.ConfigPath(h), c); err != nil {
		t.Fatal(err)
	}
	return h
}

func serversGoldenView(t *testing.T, s *serversScreen, w, h int) []string {
	t.Helper()
	v := ansi.Strip(s.View(w, h))
	lines := strings.Split(v, "\n")
	if len(lines) != h {
		t.Fatalf("view at %dx%d has %d lines, want %d", w, h, len(lines), h)
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(l, " ")
	}
	return out
}

func serversAssertGolden(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("golden has %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// Single-pane (width<100): list inner width = w-2.
func serversGoldenList(w int, rows []string) []string {
	out := []string{borderRow("╭", "─", "╮", w-2)}
	for _, r := range rows {
		out = append(out, "│"+r+strings.Repeat(" ", w-2-len([]rune(r)))+"│")
	}
	out = append(out, borderRow("╰", "─", "╯", w-2))
	return out
}

func borderRow(left, fill, right string, inner int) string {
	return left + strings.Repeat(fill, inner) + right
}

// Split pane (width>=100): left = width*35/100 (inner left-2), gap " ",
// right = width-left-1 (inner right-2).
func serversGoldenSplit(width int, leftRows, rightRows [][]string) []string {
	left := width * 35 / 100
	right := width - left - 1
	li, ri := left-2, right-2
	out := []string{
		borderRow("╭", "─", "╮", li) + " " + borderRow("╭", "─", "╮", ri),
	}
	for i := 1; i < maxLen(leftRows, rightRows)-1; i++ {
		l, r := "", ""
		if i < len(leftRows) && len(leftRows[i]) > 0 {
			l = leftRows[i][0]
		}
		if i < len(rightRows) && len(rightRows[i]) > 0 {
			r = rightRows[i][0]
		}
		out = append(out, "│"+l+strings.Repeat(" ", li-len([]rune(l)))+"│"+" "+"│"+r+strings.Repeat(" ", ri-len([]rune(r)))+"│")
	}
	out = append(out, borderRow("╰", "─", "╯", li)+" "+borderRow("╰", "─", "╯", ri))
	return out
}

func maxLen(a, b [][]string) int {
	if len(a) > len(b) {
		return len(a)
	}
	return len(b)
}

// Column header for widths [18,14,5,14] (traced columnWidths at av=75,
// inner 78): cell pads + 2-cell joiner give gaps Name->Protocol 16sp,
// Protocol->Port 8sp, Port->State 3sp.
const serversGoldenHeader80 = "  Name                Protocol        Port   State"

// Column header for the shrunk widths [5,8,5,8] (av=30 at a 35-cell pane):
// gaps 3sp / 2sp / 3sp; the row totals 34 cells and list.go clips it to
// inner 33, dropping one trailing space.
const serversGoldenHeader35 = "  Name   Protocol  Port   State"

func TestServersGoldenEmptyList(t *testing.T) {
	s := newServersTestScreen(t, serversWizardHome(t))
	serversDrain(t, s, s.Init())
	s.detail = nil
	s.syncWorkspace()

	// 80x24: single list pane, no rows, only the add line selected.
	rows := []string{
		"Servers",
		serversGoldenHeader80,
		"No servers configured",
		"› + Add a server [a]",
	}
	for i := 0; i < 17; i++ {
		rows = append(rows, "")
	}
	rows = append(rows, "1/1")
	serversAssertGolden(t, serversGoldenView(t, s, 80, 24), serversGoldenList(80, rows))

	// 100x30: left list pane (35) + placeholder card pane (64).
	// Status row: list.go puts fmtPosition at content index h-2-1 = 27,
	// i.e. frame row 28.
	left := goldenRows(30, map[int]string{
		1:  "Servers",
		2:  serversGoldenHeader35,
		3:  "› + Add a server [a]",
		28: "1/1",
	})
	right := goldenRows(30, map[int]string{
		1: "Server",
		2: "No servers configured. Add a server to start.",
		3: "  Add a server...  [a]",
		4: "  Reload  [r]",
	})
	serversAssertGolden(t, serversGoldenView(t, s, 100, 30), serversGoldenSplit(100, left, right))
}

// goldenRows builds a full-height row table with content at fixed frame rows.
func goldenRows(h int, at map[int]string) [][]string {
	out := make([][]string, h)
	for i := range out {
		out[i] = []string{""}
	}
	for i, s := range at {
		if i >= 0 && i < h {
			out[i] = []string{s}
		}
	}
	return out
}

func TestServersGoldenTwoServerList(t *testing.T) {
	s := newServersTestScreen(t, serversGoldenHome(t))
	serversDrain(t, s, s.Init())
	s.detail = nil
	s.syncWorkspace()

	// 80x24: widths [18,14,5,14]; ss row gaps 18/5/3, vl gaps 18/3/3
	// (cell padding + the 2-cell joiner).
	rows := []string{
		"Servers",
		serversGoldenHeader80,
		"› ss                  shadowsocks     1234   angel stopped",
		"  vl                  vless-reality   2345   angel stopped",
		"  + Add a server [a]",
	}
	for i := 0; i < 16; i++ {
		rows = append(rows, "")
	}
	rows = append(rows, "1/3")
	serversAssertGolden(t, serversGoldenView(t, s, 80, 24), serversGoldenList(80, rows))

	// 100x30 split: cells shrink to [5,8,5,8]; each row totals 34 cells and
	// is clipped to inner 33 (the last char of "angel stopped" is cut).
	left := goldenRows(30, map[int]string{
		1:  "Servers",
		2:  serversGoldenHeader35,
		3:  "› ss     shadowso  1234   angel s",
		4:  "  vl     vless-re  2345   angel s",
		5:  "  + Add a server [a]",
		28: "1/3",
	})
	right := goldenRows(30, map[int]string{
		1: "Server",
		2: "Select a server to see details.",
		3: "  Add a server...  [a]",
		4: "  Reload  [r]",
	})
	serversAssertGolden(t, serversGoldenView(t, s, 100, 30), serversGoldenSplit(100, left, right))
}

// ---------------------------------------------------------------------------
// G: size matrix for the real-frame dialogs (reveal, wizard, edit prompt,
// rotate/remove confirm). Structural per size: exact height and per-row
// width bound, plus content checks.
// ---------------------------------------------------------------------------

func TestServersFrameDialogSizeMatrix(t *testing.T) {
	// 60x16 and below renders the frame's "window too small" body and drops
	// all keys (frame.go View/Update width<=60 guard), so it only gets a
	// structural check; every other size drives the real dialogs.
	t.Run("too-small", func(t *testing.T) {
		f := serversTestFrame(t, serversFixture(t), 60, 16)
		v := serversFrameView(t, f)
		if !strings.Contains(v, "Window too small") {
			t.Fatalf("60x16 frame body wrong: %q", v)
		}
	})
	sizes := [][2]int{{80, 24}, {100, 30}, {120, 40}}
	for _, size := range sizes {
		w, h := size[0], size[1]
		home := serversFixture(t)
		f := serversTestFrame(t, home, w, h)
		// prep closes any dialog, reloads the ss detail and focuses the card
		// (at w<100 Enter drills into the card; at w>=100 Tab focuses it).
		prep := func() {
			t.Helper()
			serversFrameKey(t, f, tea.KeyEscape, "") // cancel any open dialog
			serversFrameKey(t, f, tea.KeyEscape, "") // undrill (<100) / screen Esc sets list focus (>=100)
			serversFrameKey(t, f, tea.KeyEnter, "")  // load detail (drills in <100)
			if w >= 100 {
				serversFrameKey(t, f, tea.KeyTab, "") // card focus
			}
		}
		check := func(stage string, content []string) {
			t.Helper()
			body := serversFrameView(t, f)
			lines := strings.Split(body, "\n")
			if len(lines) != h {
				t.Errorf("%s at %dx%d: %d lines", stage, w, h, len(lines))
			}
			for i, line := range lines {
				if aw := ansi.StringWidth(line); aw > w {
					t.Errorf("%s at %dx%d row %d width %d", stage, w, h, i, aw)
				}
			}
			for _, want := range content {
				if !strings.Contains(body, want) {
					t.Errorf("%s at %dx%d missing %q", stage, w, h, want)
				}
			}
		}
		prep()
		// Edit prompt: Enter activates the first card line (Bind).
		serversFrameKey(t, f, tea.KeyEnter, "")
		check("edit-prompt", []string{"Server ss · Bind"})
		prep()
		// Rotate confirm via env.OpenModal -> frame modal.
		serversFrameKey(t, f, 't', "t")
		check("rotate-confirm", []string{"Rotate keys for server ss", "alice"})
		serversFrameKey(t, f, tea.KeyEscape, "") // confirm.go esc cancels
		prep()
		// Remove confirm.
		serversFrameKey(t, f, 'd', "d")
		check("remove-confirm", []string{"Remove server ss"})
		serversFrameKey(t, f, tea.KeyEscape, "")
		prep()
		// Link warning then reveal.
		serversFrameKey(t, f, 'l', "l")
		check("link-warning", []string{"contains credentials", "Show"})
		serversFrameKey(t, f, 'y', "y")
		check("reveal", []string{"Client link · ss"})
		prep()
		// One more Esc: back to the list ('a' is suppressed while drilled at
		// w<100; at w>=100 Esc refocuses the list).
		serversFrameKey(t, f, tea.KeyEscape, "")
		// Wizard.
		serversFrameKey(t, f, 'a', "a")
		check("wizard", []string{"Add server", "Choose the server protocol."})
	}
}

// ---------------------------------------------------------------------------
// E: reveal shows the FULL link, only in the reveal frame, and scrolls.
// ---------------------------------------------------------------------------

// serversRevealBodyRows returns the body rows between the title row (0) and
// the indicator row (h-2), trailing padding stripped.
func serversRevealBodyRows(t *testing.T, view string, h int) []string {
	t.Helper()
	rows := strings.Split(view, "\n")
	if len(rows) != h {
		t.Fatalf("reveal has %d rows, want %d", len(rows), h)
	}
	out := make([]string, 0, h-4)
	for i := 2; i < h-2; i++ {
		out = append(out, strings.TrimRight(rows[i], " "))
	}
	return out
}

func TestServersRevealShowsFullLinkAndScrolls(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	var frames []string
	serversDrain(t, s, s.Init())
	serversLoadDetailDrained(t, s)
	link, err := ServerLink(home, "ss", ServerLinkOptions{Account: "alice", Format: "uri"})
	if err != nil || link == "" {
		t.Fatalf("client link: %v", err)
	}
	// Hand-traced: the fixture link is a non-2022 shadowsocks URI,
	// "ss://" + base64url("aes-128-gcm:<22-char password>") + "@127.0.0.1:1234#ss"
	// (~70 chars), so it fits one 120-cell row and one 80-cell row.
	if len(link) >= 80 {
		t.Fatalf("link longer than the asserted wrap width: %d", len(link))
	}
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	s.workspace.SetFocus(tui.PaneCard)
	if _, c := s.Update(serversKey('l', "l")); c != nil {
		t.Fatal("link hotkey produced a command")
	}
	warn := s.View(120, 40)
	frames = append(frames, warn)
	if strings.Contains(warn, link) {
		t.Fatal("warning frame leaked the link")
	}
	_, c := s.Update(serversKey('y', "y"))
	serversDrain(t, s, c)
	if s.dialog == nil || s.dialog.modal == nil {
		t.Fatal("reveal did not open")
	}
	reveal := s.View(120, 40)
	frames = append(frames, reveal)
	// Full link: serversMenuModal.View puts title at row 0, body from row 2,
	// buttons at h-1; slots = h-4 = 36 so nothing overflows. The URI has no
	// spaces, so serversWrapLine keeps it on one contiguous row; the exact
	// wrapped chunks must appear verbatim in the body rows.
	rows := serversRevealBodyRows(t, reveal, 40)
	chunks := serversWrapLine(link, 120)
	foundAt := -1
	for i := 0; i+len(chunks) <= len(rows); i++ {
		ok := true
		for j, ch := range chunks {
			if rows[i+j] != ch {
				ok = false
				break
			}
		}
		if ok {
			foundAt = i
			break
		}
	}
	if foundAt < 0 {
		t.Fatalf("full link not rendered verbatim in the reveal body: %q", reveal)
	}

	// Scrolling at 80x8 (same dialog instance): the body has 7 entries
	// (4 prefix + link + 2 suffix) and slots = h-4 = 4, so the modal
	// overflows with an indicator at h-2 and offsets 0..3. At offset 0 the
	// link row (body[4]) is off-screen.
	small := s.View(80, 8)
	if !strings.Contains(small, "lines 1-4 of 7") {
		t.Fatalf("scroll indicator missing at offset 0: %q", small)
	}
	if strings.Contains(small, link) {
		t.Fatal("link visible before scrolling")
	}
	// Down -> offset 1 -> "lines 2-5 of 7"; body[4] (the link) becomes visible.
	_, _ = s.Update(serversKey(tea.KeyDown, ""))
	page1 := s.View(80, 8)
	if !strings.Contains(page1, "lines 2-5 of 7") || !strings.Contains(page1, link) {
		t.Fatalf("down did not reveal the link row: %q", page1)
	}
	// End -> offset clamps to len-slots = 3 -> "lines 4-7 of 7", tail rows.
	_, _ = s.Update(serversKey(tea.KeyEnd, ""))
	pageEnd := s.View(80, 8)
	if !strings.Contains(pageEnd, "lines 4-7 of 7") || !strings.Contains(pageEnd, link) {
		t.Fatalf("end did not show the tail: %q", pageEnd)
	}

	// Esc closes; the link must not survive anywhere else.
	_, c = s.Update(serversKey(tea.KeyEscape, ""))
	serversDrain(t, s, c)
	after := s.View(120, 40)
	frames = append(frames, after)
	if strings.Contains(after, link) {
		t.Fatal("link survived the reveal close")
	}
	reveals := 0
	for _, f := range frames {
		if strings.Contains(f, link) {
			reveals++
		}
	}
	if reveals != 1 {
		t.Fatalf("link appeared in %d frames, want only the reveal", reveals)
	}
}

// ---------------------------------------------------------------------------
// F(8): YAML-only protocol — registered via the exported servers.Register
// (no unregistration exists; the unique protocol name and this file sorting
// last among the tui_servers_* test files keep other tests unaffected).
// Link always fails; MihomoLinker yields YAML, so the 'l' flow falls back
// and titles the reveal "(mihomo yaml)" (confirmServerLink fallback +
// newServersLinkModal).
// ---------------------------------------------------------------------------

type serversYAMLTestProtocol struct{}

func (serversYAMLTestProtocol) Name() string     { return "yamlsrv-test" }
func (serversYAMLTestProtocol) SingleUser() bool { return false }
func (serversYAMLTestProtocol) Validate(map[string]string) error {
	return nil
}
func (serversYAMLTestProtocol) NewMaterial(map[string]string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (serversYAMLTestProtocol) NewSecret(string, map[string]string) (map[string]string, error) {
	return map[string]string{"token": "yaml-token-material"}, nil
}
func (serversYAMLTestProtocol) Start(servers.Spec, servers.Env) (io.Closer, error) {
	return nil, nil
}
func (serversYAMLTestProtocol) Link(servers.Spec, string, string) (string, error) {
	return "", errors.New("yaml-only protocol has no uri link")
}
func (serversYAMLTestProtocol) Mihomo(servers.Spec, string, string) (string, error) {
	return "proxies:\n  - name: yaml-test\n", nil
}

func TestServersYAMLOnlyProtocolRevealsMihomoYAML(t *testing.T) {
	servers.Register(serversYAMLTestProtocol{})
	home := t.TempDir()
	c := config.Default()
	c.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"}}
	c.Servers = []config.Server{{Name: "ys", Protocol: "yamlsrv-test", Bind: "127.0.0.1", Port: 3500, PublicHost: "pub.test", Accounts: "alice"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("yaml protocol fixture invalid: %v", err)
	}
	if err := config.Save(config.ConfigPath(home), c); err != nil {
		t.Fatal(err)
	}
	specs, err := c.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if _, err := servers.NewStore(home).EnsureSecrets(spec); err != nil {
			t.Fatal(err)
		}
	}
	s := newServersTestScreen(t, home)
	serversDrain(t, s, s.Init())
	serversLoadDetailDrained(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	_ = s.View(120, 30)
	// Single account -> no account choice; direct credential warning.
	if _, c := s.Update(serversKey('l', "l")); c != nil {
		t.Fatal("link hotkey produced a command")
	}
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.ConfirmPrompt {
		t.Fatalf("warning did not open: %+v", s.dialog)
	}
	_, cm := s.Update(serversKey('y', "y"))
	serversDrain(t, s, cm)
	// confirmServerLink: Format "uri" fails -> fallback "mihomo" succeeds
	// via MihomoLinker -> serversLinkDone{format:"mihomo"} ->
	// title "Client link · ys (mihomo yaml)".
	if s.dialog == nil || s.dialog.modal == nil {
		t.Fatal("yaml fallback did not open the reveal")
	}
	v := s.View(120, 30)
	if !strings.Contains(v, "Client link · ys (mihomo yaml)") {
		t.Fatalf("mihomo yaml title missing: %q", v)
	}
	if !strings.Contains(v, "proxies:") {
		t.Fatalf("yaml body missing from the reveal: %q", v)
	}
}
