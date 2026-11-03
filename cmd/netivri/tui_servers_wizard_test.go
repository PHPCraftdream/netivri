package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
	"github.com/ktav-lang/golang/pure"
)

// serversWizardHome builds a temp home with two accounts and no servers.
func serversWizardHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	c := config.Default()
	c.Accounts = []config.Account{
		{Login: "alice", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"},
		{Login: "bob", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	if err := config.Save(config.ConfigPath(h), c); err != nil {
		t.Fatal(err)
	}
	return h
}

func serversTypeKeys(s *serversScreen, text string) {
	for _, r := range text {
		_, _ = s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}
func serversEnterKey(s *serversScreen) tea.Cmd {
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return c
}
func serversDownKeys(s *serversScreen, n int) {
	for i := 0; i < n; i++ {
		_, _ = s.Update(serversKey(tea.KeyDown, ""))
	}
}
func serversBackspaces(s *serversScreen, n int) {
	for i := 0; i < n; i++ {
		_, _ = s.Update(serversKey(tea.KeyBackspace, ""))
	}
}

// serversWizardProtocol selects a protocol in the wizard's first step with
// Down presses (the choice prompt starts at option 0) and advances with Enter.
// Hand-traced: newServerWizard step 0 is a ChoicePrompt of ServerProtocols()
// order; the servers dialog rebuilds the wizard when Values["Protocol"] changes
// (tui_servers_wizard.go updateWizard) and lands on Index 1 (Name).
func serversWizardProtocol(t *testing.T, s *serversScreen, name string) {
	t.Helper()
	d := s.dialog
	if d == nil || d.wizard == nil {
		t.Fatal("wizard is not open")
	}
	idx := -1
	for i, p := range ServerProtocols() {
		if p.Name == name {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("protocol %q not registered", name)
	}
	_ = s.View(120, 30) // lazy-initialize the choice prompt (prompt.go initialize)
	p := d.wizard.Steps[0].Prompt
	cur := 0
	for i, o := range p.Options {
		if o.Value == p.Value {
			cur = i
		}
	}
	// ChoicePrompt Down only moves p.choice; the value lands in Values on Enter.
	serversDownKeys(s, (idx-cur+len(p.Options))%len(p.Options))
	if serversEnterKey(s) != nil {
		t.Fatal("protocol advance produced a command")
	}
	if s.dialog.wizard.Values["Protocol"] != name {
		t.Fatalf("wizard protocol value = %q", s.dialog.wizard.Values["Protocol"])
	}
	if s.dialog.wizard.Index != 1 {
		t.Fatalf("after protocol step wizard index = %d", s.dialog.wizard.Index)
	}
}

func serversWizardStepKeys(w *tui.Wizard) []string {
	out := make([]string, 0, len(w.Steps))
	for _, st := range w.Steps {
		out = append(out, st.Key)
	}
	return out
}

func serversWizardParamStep(t *testing.T, w *tui.Wizard, key string) int {
	t.Helper()
	for i, st := range w.Steps {
		if st.Key == key {
			return i
		}
	}
	t.Fatalf("wizard step %q missing; steps=%v", key, serversWizardStepKeys(w))
	return -1
}

// serversSecretValues reads the server secrets store (internal/servers/store.go
// persists plaintext JSON under <home>/server-secrets.ktav) and returns every
// stored value of at least 10 characters, i.e. generated key material.
func serversSecretValues(t *testing.T, home string) []string {
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
	out := []string{}
	for _, e := range doc.Servers {
		for _, v := range e.Material {
			if len(v) >= 10 {
				out = append(out, v)
			}
		}
		for _, fields := range e.Users {
			for _, v := range fields {
				if len(v) >= 10 {
					out = append(out, v)
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("secrets store contained no key material")
	}
	return out
}

// serversAssertNoLeaks fails if any frame contains the client link or any
// stored secret value.
func serversAssertNoLeaks(t *testing.T, home, link string, frames []string) {
	t.Helper()
	for i, f := range frames {
		if link != "" && strings.Contains(f, link) {
			t.Fatalf("frame %d leaked the client link", i)
		}
	}
	for _, sec := range serversSecretValues(t, home) {
		for i, f := range frames {
			if strings.Contains(f, sec) {
				t.Fatalf("frame %d leaked stored secret material", i)
			}
		}
	}
}

func TestServersAddWizardRealKeysCreatesTwoProtocols(t *testing.T) {
	home := serversWizardHome(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	var frames []string
	snap := func() { frames = append(frames, s.View(120, 30)) }
	snap()

	// Open the wizard with the "a" hotkey (tui_servers.go key handler "a").
	if _, c := s.Update(serversKey('a', "a")); c != nil {
		t.Fatal("opening the wizard produced a command")
	}
	d := s.dialog
	if d == nil || d.wizard == nil || d.wizard.Index != 0 {
		t.Fatal("hotkey a did not open the add wizard")
	}
	snap()

	// Shadowsocks walk.
	serversWizardProtocol(t, s, "shadowsocks")
	d = s.dialog
	snap()
	if got := serversWizardParamStep(t, d.wizard, "param:cipher"); got != 5 {
		t.Fatalf("cipher step index = %d, steps=%v", got, serversWizardStepKeys(d.wizard))
	}
	serversTypeKeys(s, "wss01")
	if d.wizard.Steps[1].Prompt.Value != "wss01" {
		t.Fatalf("name input = %q", d.wizard.Steps[1].Prompt.Value)
	}
	serversEnterKey(s) // Name -> Bind
	if d.wizard.Steps[2].Prompt.Value != "0.0.0.0" {
		t.Fatalf("bind default = %q", d.wizard.Steps[2].Prompt.Value)
	}
	serversEnterKey(s) // Bind -> Port
	serversTypeKeys(s, "45701")
	serversEnterKey(s) // Port -> Public host
	serversTypeKeys(s, "link.test")
	serversEnterKey(s) // Public host -> Cipher (choice, default kept)
	if d.wizard.Steps[5].Prompt.Value != "2022-blake3-aes-128-gcm" {
		t.Fatalf("cipher default = %q", d.wizard.Steps[5].Prompt.Value)
	}
	serversEnterKey(s) // Cipher -> UDP relay (text, default false)
	if d.wizard.Steps[6].Prompt.Value != "false" {
		t.Fatalf("udp default = %q", d.wizard.Steps[6].Prompt.Value)
	}
	serversEnterKey(s) // UDP -> Accounts (multi choice, all accounts preselected)
	acct := d.wizard.Steps[7]
	if acct.Key != "Accounts" || strings.Join(acct.Prompt.Selected, ",") != "alice,bob" {
		t.Fatalf("accounts step = %q selected=%v", acct.Key, acct.Prompt.Selected)
	}
	serversEnterKey(s) // Accounts -> review
	if !d.wizard.Review {
		t.Fatal("last step enter did not reach review")
	}
	review := s.View(120, 30)
	snap()
	for _, want := range []string{"Review", "Create", "Name: wss01", "Port: 45701", "Bind address: 0.0.0.0", "Accounts: alice,bob"} {
		if !strings.Contains(review, want) {
			t.Fatalf("review missing %q: %q", want, review)
		}
	}

	// Review Enter focuses [Create] and returns the wrapped wizard result.
	c := serversEnterKey(s)
	if c == nil {
		t.Fatal("review create did not return a result command")
	}
	msg := c()
	res, ok := msg.(serversDialogResult)
	if !ok || res.Canceled {
		t.Fatalf("review create produced %#v", msg)
	}
	_, c = s.Update(msg)
	if c == nil || !d.wizard.Saving {
		t.Fatal("dialog result did not start server creation")
	}
	serversRun(t, s, c)
	if s.dialog == nil || s.dialog.modal == nil {
		t.Fatal("successful add did not open the result modal")
	}
	snap()
	added := s.View(120, 30)
	for _, want := range []string{"Server added", "Name: wss01", "Protocol: shadowsocks", "Port: 45701", "Accounts: alice,bob", "Server secrets were generated and stored securely."} {
		if !strings.Contains(added, want) {
			t.Fatalf("added modal missing %q: %q", want, added)
		}
	}
	// Close the added modal with Esc (serversMenuModal esc -> serversModalAction{-1}).
	_, c = s.Update(serversKey(tea.KeyEscape, ""))
	serversRun(t, s, c)
	if s.dialog != nil {
		t.Fatal("esc did not close the added modal")
	}

	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cfg.GetServer("wss01")
	if entry == nil || entry.Protocol != "shadowsocks" || entry.Port != 45701 || entry.Bind != "0.0.0.0" || entry.Accounts != "alice,bob" || entry.PublicHost != "link.test" {
		t.Fatalf("stored server wrong: %#v", entry)
	}
	if entry.Params["cipher"] != "2022-blake3-aes-128-gcm" || entry.Params["udp"] != "false" {
		t.Fatalf("stored params wrong: %#v", entry.Params)
	}

	// Mieru walk against the same home.
	if _, c := s.Update(serversKey('a', "a")); c != nil {
		t.Fatal("reopening the wizard produced a command")
	}
	serversWizardProtocol(t, s, "mieru")
	d = s.dialog
	if serversWizardParamStep(t, d.wizard, "param:transport") != 5 || serversWizardParamStep(t, d.wizard, "param:multiplexing") != 6 {
		t.Fatalf("mieru steps wrong: %v", serversWizardStepKeys(d.wizard))
	}
	serversTypeKeys(s, "wmieru01")
	serversEnterKey(s) // Name -> Bind
	serversEnterKey(s) // Bind -> Port
	serversTypeKeys(s, "45702")
	serversEnterKey(s) // Port -> Public host (left empty)
	serversEnterKey(s) // Public host -> Transport (TCP)
	if d.wizard.Steps[5].Prompt.Value != "TCP" {
		t.Fatalf("transport default = %q", d.wizard.Steps[5].Prompt.Value)
	}
	serversEnterKey(s) // Transport -> Multiplexing
	if d.wizard.Steps[6].Prompt.Value != "MULTIPLEXING_DEFAULT" {
		t.Fatalf("multiplexing default = %q", d.wizard.Steps[6].Prompt.Value)
	}
	serversEnterKey(s) // Multiplexing -> Accounts
	serversEnterKey(s) // Accounts -> review
	if !d.wizard.Review {
		t.Fatal("mieru walk did not reach review")
	}
	snap()
	if c = serversEnterKey(s); c == nil {
		t.Fatal("mieru review create did not return a command")
	}
	if msg = c(); msg == nil {
		t.Fatal("mieru review produced no message")
	}
	if _, c = s.Update(msg); c == nil {
		t.Fatal("mieru dialog result did not start creation")
	}
	serversRun(t, s, c)
	snap()
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ = cfg.GetServer("wmieru01")
	if entry == nil || entry.Protocol != "mieru" || entry.Port != 45702 || entry.Params["transport"] != "TCP" || entry.Params["multiplexing"] != "MULTIPLEXING_DEFAULT" {
		t.Fatalf("stored mieru server wrong: %#v", entry)
	}

	// The secrets store holds generated material for both servers.
	linkSS, err := ServerLink(home, "wss01", ServerLinkOptions{Account: "alice", Format: "uri"})
	if err != nil || linkSS == "" {
		t.Fatalf("client link: %v", err)
	}
	serversAssertNoLeaks(t, home, linkSS, frames)
}

func TestServersWizardBackChangesProtocolAndRebuildsSteps(t *testing.T) {
	home := serversWizardHome(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	_, _ = s.Update(serversKey('a', "a"))
	serversWizardProtocol(t, s, "shadowsocks")
	d := s.dialog
	serversTypeKeys(s, "keepname")
	serversEnterKey(s) // Name -> Bind
	serversEnterKey(s) // Bind -> Port
	serversEnterKey(s) // Port -> Public host
	serversEnterKey(s) // Public host -> Cipher (a shadowsocks param step)
	if got := d.wizard.Index; got != serversWizardParamStep(t, d.wizard, "param:cipher") {
		t.Fatalf("did not reach the cipher step: %d %v", got, serversWizardStepKeys(d.wizard))
	}
	// Walk Back to the protocol step: Tab focuses [Back] once, then Enter
	// keeps activating the still-focused Back button (wizard.go activate(0)
	// decrements Index without validation and does not reset button focus).
	_, _ = s.Update(serversKey(tea.KeyTab, ""))
	for i := 0; i < 5; i++ {
		if serversEnterKey(s) != nil {
			t.Fatal("back produced a command")
		}
	}
	d = s.dialog
	if d.wizard.Index != 0 || d.wizard.Steps[0].Prompt.Value != "shadowsocks" {
		t.Fatalf("back did not reach the protocol step: %d %q", d.wizard.Index, d.wizard.Steps[0].Prompt.Value)
	}
	if d.wizard.Steps[1].Prompt.Value != "keepname" {
		t.Fatalf("back lost the name answer: %q", d.wizard.Steps[1].Prompt.Value)
	}
	// The two tabs leave the button row (the Back walk left it keyboard-focused
	// on [Back]); serversWizardProtocol then requires prompt key handling.
	_, _ = s.Update(serversKey(tea.KeyTab, ""))
	_, _ = s.Update(serversKey(tea.KeyTab, ""))
	// Choose mieru; the dialog rebuilds the wizard (updateWizard protocol change).
	serversWizardProtocol(t, s, "mieru")
	d = s.dialog
	stepKeys := serversWizardStepKeys(d.wizard)
	if strings.Join(stepKeys, ",") != strings.Join([]string{"Protocol", "Name", "Bind", "Port", "PublicHost", "param:transport", "param:multiplexing", "Accounts"}, ",") {
		t.Fatalf("rebuilt steps are not mieru's: %v", stepKeys)
	}
	for _, st := range d.wizard.Steps {
		if st.Key == "param:cipher" || strings.Contains(st.Prompt.TitleText, "Cipher") {
			t.Fatal("stale shadowsocks param step survived the protocol change")
		}
	}
	if d.wizard.Steps[1].Prompt.Value != "keepname" {
		t.Fatalf("name answer lost across rebuild: %q", d.wizard.Steps[1].Prompt.Value)
	}
	if d.wizard.Steps[2].Prompt.Value != "0.0.0.0" {
		t.Fatalf("bind answer lost across rebuild: %q", d.wizard.Steps[2].Prompt.Value)
	}
	opts := serverAddOptionsFromWizard(d.wizard.Values)
	if _, ok := opts.Params["cipher"]; ok {
		t.Fatal("stale cipher param reached the add options")
	}
	if opts.Protocol != "mieru" || opts.Name != "keepname" || opts.Bind != "0.0.0.0" {
		t.Fatalf("add options wrong: %+v", opts)
	}
}

func TestServersWizardInvalidNameRetainsAnswerAndStaysOpen(t *testing.T) {
	home := serversWizardHome(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	_, _ = s.Update(serversKey('a', "a"))
	d := s.dialog
	// Accept the default protocol; the dialog rebuilds onto the Name step.
	serversEnterKey(s)
	d = s.dialog
	serversTypeKeys(s, "bad name")
	if serversEnterKey(s) != nil {
		t.Fatal("invalid name advance produced a command")
	}
	if s.dialog != d || d.wizard.Index != 1 || d.wizard.Review || d.wizard.Saving {
		t.Fatal("invalid name closed or advanced the wizard")
	}
	if d.wizard.Error == nil || !strings.Contains(d.wizard.Error.Error(), "name must be 1 to 32") {
		t.Fatalf("validation error missing: %v", d.wizard.Error)
	}
	if d.wizard.Steps[1].Prompt.Value != "bad name" {
		t.Fatalf("typed answer was discarded: %q", d.wizard.Steps[1].Prompt.Value)
	}
	v := s.View(120, 30)
	if !strings.Contains(v, "Error:") || !strings.Contains(v, "bad name") {
		t.Fatalf("frame does not show the retained error and answer: %q", v)
	}
}

func TestServersEditPortViaMouseClickSavesThroughPrompt(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	_ = s.View(120, 30) // lay out the card so Info/Lines positions are final

	portIdx := -1
	for i, l := range s.workspace.Card.Lines {
		if l.ID == "port" {
			portIdx = i
		}
	}
	if portIdx < 0 {
		t.Fatal("port line missing from card")
	}
	// Card layout (card.go): border(1) + title(1) + len(Info) rows precede the
	// lines; the workspace offsets the card pane by left+1 columns.
	rowY := 1 + 1 + len(s.workspace.Card.Info) + portIdx
	left := 120 * 35 / 100
	offsetX := left + 1
	prefix := "  Port  1234"
	editStart := 1 + ansi.StringWidth(prefix) + 2
	clickX := offsetX + editStart + 2 // inside the [edit] range of the port line
	_, _ = s.Update(tea.MouseClickMsg{X: clickX, Y: rowY, Button: tea.MouseLeft})
	if s.dialog == nil || s.dialog.prompt == nil {
		t.Fatalf("field click did not open the port prompt (dialog=%v)", s.dialog != nil)
	}
	if s.dialog.prompt.Kind != tui.NumberPrompt || s.dialog.prompt.Value != "1234" {
		t.Fatalf("port prompt = %v %q", s.dialog.prompt.Kind, s.dialog.prompt.Value)
	}
	serversBackspaces(s, 4)
	serversTypeKeys(s, "4599")
	if s.dialog.prompt.Value != "4599" {
		t.Fatalf("prompt value = %q", s.dialog.prompt.Value)
	}
	// Render at height 26 so the prompt buttons sit on y=25; [ Save ] spans x 0..9.
	_ = s.View(120, 26)
	_, cmd := s.Update(tea.MouseClickMsg{X: 4, Y: 25, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("Save click did not produce a result command")
	}
	msg := cmd()
	res, ok := msg.(serversDialogResult)
	if !ok || res.Canceled || res.Values["value"] != "4599" {
		t.Fatalf("save command produced %#v", msg)
	}
	_, cmd = s.Update(msg)
	serversRun(t, s, cmd)
	if s.dialog != nil {
		t.Fatal("save did not close the prompt")
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cfg.GetServer("ss")
	if entry == nil || entry.Port != 4599 {
		t.Fatalf("port edit did not persist: %#v", entry)
	}
	if !strings.Contains(s.View(120, 30), "4599") {
		t.Fatal("card did not show the new port")
	}
}

func TestServersLinkRevealAppearsOnlyInRevealFrame(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)

	link, err := ServerLink(home, "ss", ServerLinkOptions{Account: "alice", Format: "uri"})
	if err != nil || link == "" {
		t.Fatalf("client link: %v", err)
	}

	var frames []string
	snap := func() { frames = append(frames, s.View(120, 30)) }
	snap() // list + card, no dialog
	if strings.Contains(frames[0], link) {
		t.Fatal("list/card frame already contains the link")
	}
	// Hotkey l opens the warning confirm before any link is fetched.
	if _, c := s.Update(serversKey('l', "l")); c != nil {
		t.Fatal("link hotkey produced a command")
	}
	d := s.dialog
	if d == nil || d.prompt == nil || d.prompt.Kind != tui.ConfirmPrompt {
		t.Fatalf("hotkey l did not open the warning dialog: %+v", s.dialog)
	}
	warn := s.View(120, 30)
	snap()
	if !strings.Contains(warn, "contains credentials") || !strings.Contains(warn, "Show") || !strings.Contains(warn, "Cancel") {
		t.Fatalf("warning frame wrong: %q", warn)
	}
	if strings.Contains(warn, link) {
		t.Fatal("warning frame leaked the link")
	}
	// ConfirmPrompt defaults to Cancel (Focus 0); "y" picks Show
	// (prompt.go confirmButton(1) -> result(false)).
	_, cmd := s.Update(serversKey('y', "y"))
	if cmd == nil {
		t.Fatal("show did not return a command")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("show produced no message")
	} else {
		res, ok := msg.(serversDialogResult)
		if !ok || res.Canceled {
			t.Fatalf("show result = %#v", msg)
		}
		_, cmd = s.Update(msg)
	}
	serversRun(t, s, cmd)
	if s.dialog == nil || s.dialog.modal == nil {
		t.Fatal("link result did not open the reveal modal")
	}
	reveal := s.View(120, 30)
	frames = append(frames, reveal)
	// The modal truncates lines at the frame width; assert on the visible prefix.
	linkShown := link
	if r := []rune(link); len(r) > 100 {
		linkShown = string(r[:100])
	}
	if !strings.Contains(reveal, "Client link · ss") || !strings.Contains(reveal, linkShown) || !strings.Contains(reveal, "Close") {
		t.Fatalf("reveal frame wrong: %q", reveal)
	}
	// Esc closes the reveal modal (serversMenuModal esc -> serversModalAction{-1}).
	_, cmd = s.Update(serversKey(tea.KeyEscape, ""))
	serversRun(t, s, cmd)
	after := s.View(120, 30)
	frames = append(frames, after)
	if strings.Contains(after, link) {
		t.Fatal("link survived the reveal close")
	}
	reveals := 0
	for _, f := range frames {
		if strings.Contains(f, linkShown) {
			reveals++
		}
	}
	if reveals != 1 {
		t.Fatalf("link appeared in %d frames, want only the reveal dialog", reveals)
	}
	serversAssertNoLeaks(t, home, link, frames[:len(frames)-2]) // everything except reveal + after-close still must not leak secrets
}

func TestServersRotateAndRemoveConfirms(t *testing.T) {
	home := serversFixture(t)
	// Materialize the secrets so rotation has something to rotate.
	if _, err := ServerLink(home, "ss", ServerLinkOptions{Account: "alice", Format: "uri"}); err != nil {
		t.Fatal(err)
	}
	s := newServersTestScreen(t, home)
	var toasts []string
	s.env.Toast = func(text string) tea.Cmd { toasts = append(toasts, text); return nil }
	var modal *tui.Confirm
	s.env.OpenModal = func(m tui.Modal) tea.Cmd {
		if c, ok := m.(*tui.Confirm); ok {
			modal = c
		}
		return func() tea.Msg { return m }
	}
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)

	// Rotate: confirm names the server and the affected account.
	if _, c := s.Update(serversKey('k', "k")); c != nil {
		t.Fatal("rotate hotkey produced a command")
	}
	if modal == nil {
		t.Fatal("rotate did not open a confirm modal")
	}
	if got := modal.Prompt; !strings.Contains(got, "Rotate keys for server ss") || !strings.Contains(got, "alice") {
		t.Fatalf("rotate prompt = %q", got)
	}
	// Default is No: Confirm.selected == 0, so Enter cancels (confirm.go).
	if _, c := modal.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); c == nil {
		t.Fatal("confirm enter produced no command")
	} else if m := c(); m != nil {
		r, ok := m.(tui.ConfirmResultMsg)
		if !ok || r.Confirmed || !r.Canceled {
			t.Fatalf("default confirm result = %#v", m)
		}
		_, _ = s.Update(r)
	}
	modal = nil

	// Confirmed rotate runs the op and toasts.
	if _, c := s.Update(serversKey('k', "k")); c != nil {
		t.Fatal("second rotate hotkey produced a command")
	}
	if modal == nil {
		t.Fatal("rotate confirm did not reopen")
	}
	_, cmd := s.Update(tui.ConfirmResultMsg{Confirmed: true})
	if cmd == nil {
		t.Fatal("confirmed rotate produced no command")
	}
	serversRun(t, s, cmd)
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.GetServer("ss"); i < 0 {
		t.Fatal("rotate removed the server entry")
	}
	found := false
	for _, x := range toasts {
		if strings.Contains(x, "Keys rotated for ss") {
			found = true
		}
	}
	if !found {
		t.Fatalf("rotate toast missing: %v", toasts)
	}
	modal = nil

	// Remove: confirm names the server and the account; confirming removes it.
	if _, c := s.Update(serversKey('d', "d")); c != nil {
		t.Fatal("remove hotkey produced a command")
	}
	if modal == nil {
		t.Fatal("remove did not open a confirm modal")
	}
	if got := modal.Prompt; !strings.Contains(got, "Remove server ss") || !strings.Contains(got, "alice") {
		t.Fatalf("remove prompt = %q", got)
	}
	_, cmd = s.Update(tui.ConfirmResultMsg{Confirmed: true})
	serversRun(t, s, cmd)
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, i := cfg.GetServer("ss"); i >= 0 {
		t.Fatal("confirmed remove did not delete the server")
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("unrelated server lost: %#v", cfg.Servers)
	}
	if s.dialog != nil {
		t.Fatal("remove left a dialog open")
	}
	found = false
	for _, x := range toasts {
		if strings.Contains(x, "Server removed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("remove toast missing: %v", toasts)
	}
	if _, err := os.Stat(filepath.Join(home, "server-secrets.ktav")); err == nil {
		var doc struct {
			Servers map[string]struct{} `json:"servers"`
		}
		b, rerr := os.ReadFile(filepath.Join(home, "server-secrets.ktav"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if perr := pure.LoadsInto(string(b), &doc); perr != nil {
			t.Fatal(perr)
		}
		if _, ok := doc.Servers["ss"]; ok {
			t.Fatal("removed server secrets survived")
		}
	}
}

func TestServersAngelStoppedEditSavesAndReportsPendingApply(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home) // status source errors -> angel stopped
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.workspace.SetFocus(tui.PaneCard)
	v := s.View(120, 30)
	if !strings.Contains(v, "angel stopped") || !strings.Contains(v, "State: angel stopped") {
		t.Fatalf("stopped state missing: %q", v)
	}
	if !strings.Contains(v, "Changes saved; applied when angel starts") {
		t.Fatalf("pending-apply info line missing: %q", v)
	}
	// Edit the port with card focus: Down selects the port line, Enter opens it.
	_, _ = s.Update(serversKey(tea.KeyDown, ""))
	_, cmd := s.Update(serversKey(tea.KeyEnter, ""))
	if s.dialog == nil || s.dialog.prompt == nil {
		t.Fatalf("enter did not open the port prompt: %+v", s.dialog)
	}
	serversBackspaces(s, 4)
	serversTypeKeys(s, "4600")
	_ = s.View(120, 26)
	_, cmd = s.Update(tea.MouseClickMsg{X: 4, Y: 25, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("save click produced no command")
	}
	if msg := cmd(); msg != nil {
		if _, cmd = s.Update(msg); cmd == nil {
			t.Fatal("save result did not start the update")
		}
		serversRun(t, s, cmd)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cfg.GetServer("ss")
	if entry == nil || entry.Port != 4600 {
		t.Fatalf("edit while angel stopped did not persist: %#v", entry)
	}
	after := s.View(120, 30)
	if !strings.Contains(after, "Changes saved; applied when angel starts") {
		t.Fatalf("pending-apply line lost after save: %q", after)
	}
	if !strings.Contains(after, "4600") {
		t.Fatal("card did not show the saved port")
	}
}

func TestServersFrameContractAndWeakGoldens(t *testing.T) {
	home := serversFixture(t)
	loaded := newServersTestScreen(t, home)
	serversRun(t, loaded, loaded.Init())
	serversLoadDetail(t, loaded)

	empty := newServersTestScreen(t, serversWizardHome(t))
	serversRun(t, empty, empty.Init())

	wizard := newServersTestScreen(t, serversWizardHome(t))
	serversRun(t, wizard, wizard.Init())
	_, _ = wizard.Update(serversKey('a', "a"))
	if wizard.dialog == nil || wizard.dialog.wizard == nil {
		t.Fatal("wizard state not open")
	}

	edit := newServersTestScreen(t, home)
	serversRun(t, edit, edit.Init())
	serversLoadDetail(t, edit)
	if cmd := edit.cardAction("port"); cmd != nil || edit.dialog == nil || edit.dialog.prompt == nil {
		t.Fatal("port edit dialog did not open")
	}

	reveal := newServersTestScreen(t, home)
	serversRun(t, reveal, reveal.Init())
	serversLoadDetail(t, reveal)
	reveal.detailAccounts = []string{"alice"}
	if cmd := reveal.beginShowLink("ss"); cmd != nil || reveal.dialog == nil || reveal.dialog.prompt == nil {
		t.Fatal("link warning dialog did not open")
	}

	states := []struct {
		name    string
		screen  *serversScreen
		goldens []string
	}{
		{"empty", empty, []string{"No servers configured", "+ Add a server [a]", "Name", "Reload"}},
		{"two-servers", loaded, []string{"shadowsocks", "vless-reality", "1234", "angel stopped", "Protocol", "Show client link...", "Rotate keys...", "Remove...", "[l]", "[k]", "[d]"}},
		{"wizard", wizard, []string{"Add server", "Shadowsocks", "Choose the server protocol."}},
		{"edit-port", edit, []string{"Server ss · Port", "Port"}},
		{"link-warning", reveal, []string{"Show client link", "contains credentials"}},
	}
	sizes := [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}}
	for _, st := range states {
		for _, size := range sizes {
			v := st.screen.View(size[0], size[1])
			lines := strings.Split(v, "\n")
			if len(lines) != size[1] {
				t.Errorf("%s at %dx%d: %d lines", st.name, size[0], size[1], len(lines))
			}
			for i, line := range lines {
				if w := ansi.StringWidth(ansi.Strip(line)); w > size[0] {
					t.Errorf("%s at %dx%d row %d width %d", st.name, size[0], size[1], i, w)
				}
			}
		}
		v := st.screen.View(100, 30)
		for _, want := range st.goldens {
			if !strings.Contains(v, want) {
				t.Errorf("%s golden missing %q", st.name, want)
			}
		}
	}

	// The AutoGenerated wizard steps never occur for registered protocols
	// (internal/servers/params.go marks none AutoGenerated), so no review row
	// ever shows the "generated automatically" placeholder today.
	for _, p := range ServerProtocols() {
		for _, q := range p.Parameters {
			if q.AutoGenerated {
				t.Errorf("protocol %s param %s is AutoGenerated; wizard gen-step tests must cover it", p.Name, q.Name)
			}
		}
	}
}
