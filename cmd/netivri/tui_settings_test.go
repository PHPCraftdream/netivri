package main

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

var settingsKeys = []string{
	"listen.bind", "listen.port", "listen.allowed_clients",
	"auth.login", "auth.password", "auth.allow_init_remote",
	"fetch.proxy", "fetch.update_interval",
	"chain.pre_socks5", "chain.post_socks5",
}

type settingsFixture struct {
	s      *settingsScreen
	cfg    *config.Config
	toasts []string
	calls  [][2]string
	setRes ConfigSetResult
	setErr error
}

func newSettingsFixture(t *testing.T) *settingsFixture {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatalf("save fixture config: %v", err)
	}
	f := &settingsFixture{cfg: cfg}
	env := tui.Env{
		Home:       home,
		Background: inlineBackground,
		Toast:      func(text string) tea.Cmd { f.toasts = append(f.toasts, text); return nil },
	}
	f.s = newSettingsScreen(env.Home, env).(*settingsScreen)
	f.s.load = func(string) (*config.Config, error) { return f.cfg, nil }
	f.s.set = func(_, key, value string) (ConfigSetResult, error) {
		f.calls = append(f.calls, [2]string{key, value})
		return f.setRes, f.setErr
	}
	f.drain(f.s.Init())
	return f
}

func (f *settingsFixture) drain(c tea.Cmd) {
	f.s = parityDrain(f.s, c).(*settingsScreen)
}

func (f *settingsFixture) key(k tea.KeyPressMsg) {
	sc, c := f.s.Update(k)
	f.s = sc.(*settingsScreen)
	f.drain(c)
}

func (f *settingsFixture) typeKeys(text string) {
	for _, r := range text {
		f.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// clearPrompt deletes the preset value of the open prompt with backspaces,
// like a user emptying the field before typing a new value.
func (f *settingsFixture) clearPrompt() {
	for i := 0; i < 64 && f.s.prompt != nil && f.s.prompt.Value != ""; i++ {
		f.key(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
}

// activate focuses the card pane, selects the line with the given id and
// presses Enter, like a user opening the field for editing.
func (f *settingsFixture) activate(t *testing.T, id string) {
	f.selectGroup(strings.SplitN(id, ".", 2)[0])
	f.s.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	f.s.workspace.SetFocus(tui.PaneCard)
	f.s.refresh()
	card := f.s.workspace.Card
	found := false
	for i, l := range card.Lines {
		if l.ID == id {
			card.Selected = i
			found = true
		}
	}
	if !found {
		t.Fatalf("card has no line %q", id)
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func (f *settingsFixture) selectGroup(group string) {
	for i, g := range f.s.groups {
		if g == group {
			f.s.list.Selected = i
			f.s.selected = group
			f.s.refresh()
			return
		}
	}
}

func TestSettingsListShowsGroupsAndSafeValues(t *testing.T) {
	f := newSettingsFixture(t)
	if f.s.selected != "listen" {
		t.Fatalf("selected group = %q, want listen", f.s.selected)
	}
	if len(f.s.list.Rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(f.s.list.Rows))
	}
	for i, want := range [][2]string{{"Listen", "3 keys"}, {"Auth", "3 keys"}, {"Fetch", "2 keys"}, {"Chain", "2 keys"}} {
		got := f.s.list.Rows[i].Cells
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("row %d = %v, want %v", i, got, want)
		}
	}
	c := f.s.workspace.Card
	if c.Title != "Listen" {
		t.Fatalf("card title = %q", c.Title)
	}
	if l, _ := cardLine(c, "listen.port"); l.Value != "7890" {
		t.Errorf("listen.port = %q", l.Value)
	}
	if l, _ := cardLine(c, "listen.bind"); l.Value != "127.0.0.1" {
		t.Errorf("listen.bind = %q", l.Value)
	}
	f.selectGroup("auth")
	if l, _ := cardLine(f.s.workspace.Card, "auth.password"); l.Value != "not set" {
		t.Errorf("auth.password = %q, want %q", l.Value, "not set")
	}
	f.selectGroup("chain")
	if l, _ := cardLine(f.s.workspace.Card, "chain.pre_socks5"); l.Value != "-" {
		t.Errorf("chain.pre_socks5 = %q, want %q", l.Value, "-")
	}
	if l, _ := cardLine(f.s.workspace.Card, "chain.post_socks5"); l.Value != "-" {
		t.Errorf("chain.post_socks5 = %q, want %q", l.Value, "-")
	}
}

func TestSettingsMasksSecrets(t *testing.T) {
	f := newSettingsFixture(t)
	hash, err := auth.Hash("s3cret")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	f.cfg.Auth = config.Auth{Login: "alice", Password: hash}
	f.cfg.Chain = config.Chain{PreSOCKS5: config.HopList{"socks5://alice:s3cret@relay.example:1080"}}
	f.s.refresh()
	f.selectGroup("auth")
	if l, _ := cardLine(f.s.workspace.Card, "auth.password"); l.Value != "set" {
		t.Fatalf("auth.password = %q, want %q", l.Value, "set")
	}
	// the auth card shows the hash only as "set": never the hash itself
	if v := ansi.Strip(f.s.View(80, 24)); strings.Contains(v, hash) || strings.Contains(v, "s3cret") {
		t.Fatal("the auth card leaked a credential")
	}
	f.selectGroup("chain")
	if l, _ := cardLine(f.s.workspace.Card, "chain.pre_socks5"); l.Value != "1 alternative: [socks5://relay.example:1080]" {
		t.Fatalf("chain.pre_socks5 = %q", l.Value)
	}
	v := ansi.Strip(f.s.View(100, 30)) // two panes from 100 columns
	if !strings.Contains(v, "1 alternative: [socks5://relay.example:1080]") {
		t.Fatal("the chain card did not show the redacted proxy list")
	}
	for _, secret := range []string{hash, "s3cret", "alice"} {
		if strings.Contains(v, secret) {
			t.Fatal("the chain card leaked credential text")
		}
	}
}

type settingsPromptCase struct {
	id        string
	kind      tui.PromptKind
	value     string
	skipValue bool // the raw preset depends on the file, not asserted here
}

func TestSettingsDisplaysOrderedHopArraysRedactedAndFetchProxyScalar(t *testing.T) {
	f := newSettingsFixture(t)
	f.cfg.Fetch.Proxy = "http://fetch-user:fetch-secret@fetch.example:8080"
	f.cfg.Chain.PreSOCKS5 = config.HopList{
		"socks5://pre-user:pre-secret@pre.example:1080",
		"https://post-user:post-secret@post.example:443",
	}
	f.cfg.Chain.PostSOCKS5 = config.HopList{"http://post-user:post-secret@post.example:3128"}
	f.s.refresh()

	for _, tc := range []struct {
		group, key, want string
	}{
		{"fetch", "fetch.proxy", "http://fetch.example:8080"},
		{"chain", "chain.pre_socks5", "2 alternatives: [socks5://pre.example:1080, https://post.example:443]"},
		{"chain", "chain.post_socks5", "1 alternative: [http://post.example:3128]"},
	} {
		f.selectGroup(tc.group)
		line, ok := cardLine(f.s.workspace.Card, tc.key)
		if !ok || line.Value != tc.want {
			t.Errorf("%s display did not match expected redacted value", tc.key)
		}
		view := ansi.Strip(f.s.View(100, 30))
		for _, secret := range []string{"fetch-user", "fetch-secret", "pre-user", "pre-secret", "post-user", "post-secret"} {
			if strings.Contains(view, secret) {
				t.Errorf("%s display leaked credential text", tc.key)
			}
		}
	}
	f.cfg.Chain.PostSOCKS5 = config.HopList{"none"}
	f.s.refresh()
	f.selectGroup("chain")
	if line, ok := cardLine(f.s.workspace.Card, "chain.post_socks5"); !ok || line.Value != "none" {
		t.Errorf("disabled chain.post_socks5 display = %q, want none", line.Value)
	}
}

func TestSettingsProxyPromptShowsRedactedCurrentValueWithoutPrefill(t *testing.T) {
	f := newSettingsFixture(t)
	f.cfg.Fetch.Proxy = "http://fetch-user:fetch-secret@fetch.example:8080"
	f.cfg.Chain.PreSOCKS5 = config.HopList{"socks5://hop-user:hop-secret@relay.example:1080"}
	f.cfg.Chain.PostSOCKS5 = config.HopList{"https://hop-user:hop-secret@relay.example:443", "http://other.example:3128"}
	f.s.refresh()

	for _, tc := range []struct {
		id, current string
	}{
		{"fetch.proxy", "current: http://fetch.example:8080"},
		{"chain.pre_socks5", "current: 1 alternative: [socks5://relay.example:1080]"},
		{"chain.post_socks5", "current: 2 alternatives: [https://relay.example:443, http://other.example:3128]"},
	} {
		f.activate(t, tc.id)
		if f.s.prompt == nil || f.s.prompt.Kind != tui.SecretPrompt || f.s.prompt.Value != "" {
			t.Errorf("%s prompt must be secret and have no prefill", tc.id)
		} else if !strings.Contains(f.s.prompt.Hint, tc.current) {
			t.Errorf("%s prompt did not show the expected redacted current value", tc.id)
		}
		if f.s.prompt != nil && (strings.Contains(f.s.prompt.Hint, "hop-secret") || strings.Contains(f.s.prompt.Hint, "fetch-secret")) {
			t.Errorf("%s prompt hint leaked credential text", tc.id)
		}
		f.s.dialog, f.s.prompt, f.s.promptKey = nil, nil, ""
	}
}

func TestSettingsFieldOpensTheRightPrompt(t *testing.T) {
	f := newSettingsFixture(t)
	cases := []settingsPromptCase{
		{id: "listen.bind", kind: tui.TextPrompt, value: "127.0.0.1"},
		{id: "listen.port", kind: tui.NumberPrompt, value: "7890"},
		{id: "listen.allowed_clients", kind: tui.TextPrompt, skipValue: true},
		{id: "auth.login", kind: tui.TextPrompt, value: ""},
		{id: "auth.password", kind: tui.SecretPrompt, value: ""},
		{id: "auth.allow_init_remote", kind: tui.ChoicePrompt, value: "false"},
		{id: "fetch.proxy", kind: tui.SecretPrompt, value: ""},
		{id: "fetch.update_interval", kind: tui.TextPrompt, skipValue: true},
		{id: "chain.pre_socks5", kind: tui.SecretPrompt, value: ""},
		{id: "chain.post_socks5", kind: tui.SecretPrompt, value: ""},
	}
	for _, tc := range cases {
		f.s.dialog, f.s.prompt, f.s.promptKey = nil, nil, ""
		f.activate(t, tc.id)
		p := f.s.prompt
		if p == nil {
			t.Fatalf("%s: no prompt opened", tc.id)
		}
		if p.Kind != tc.kind {
			t.Errorf("%s: kind = %d, want %d", tc.id, p.Kind, tc.kind)
		}
		if p.Title() != "Settings · "+tc.id {
			t.Errorf("%s: title = %q", tc.id, p.Title())
		}
		if !tc.skipValue && p.Value != tc.value {
			t.Errorf("%s: preset value = %q, want %q", tc.id, p.Value, tc.value)
		}
		if tc.id == "listen.port" {
			if p.Min == nil || *p.Min != 1 || p.Max == nil || *p.Max != 65535 {
				t.Errorf("%s: min/max = %v/%v", tc.id, p.Min, p.Max)
			}
		}
		if tc.kind == tui.ChoicePrompt {
			if len(p.Options) != 2 || p.Options[0].Value != "true" || p.Options[1].Value != "false" {
				t.Errorf("%s: options = %+v", tc.id, p.Options)
			}
		}
		if tc.kind == tui.SecretPrompt && p.Value != "" {
			t.Errorf("%s: secret must never be preset, got %q", tc.id, p.Value)
		}
	}
}

func TestSettingsPortEditSavesAndToastsRestart(t *testing.T) {
	f := newSettingsFixture(t)
	f.setRes = ConfigSetResult{Key: "listen.port", Changed: true, RestartNeeded: true}
	f.activate(t, "listen.port")
	f.clearPrompt()
	f.typeKeys("8080")
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(f.calls) != 1 || f.calls[0] != [2]string{"listen.port", "8080"} {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.s.dialog != nil {
		t.Fatal("a successful save must close the prompt")
	}
	if got := f.toasts[len(f.toasts)-1]; got != "Settings saved: listen.port; restart the angel to apply it" {
		t.Fatalf("toast = %q", got)
	}
}

func TestSettingsBoolEditDownThenEnterSavesTrue(t *testing.T) {
	f := newSettingsFixture(t)
	f.activate(t, "auth.allow_init_remote")
	f.key(tea.KeyPressMsg{Code: tea.KeyDown})
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(f.calls) != 1 || f.calls[0] != [2]string{"auth.allow_init_remote", "true"} {
		t.Fatalf("calls = %v", f.calls)
	}
	if got := f.toasts[len(f.toasts)-1]; got != "Settings saved: auth.allow_init_remote" {
		t.Fatalf("toast = %q, want no restart sentence", got)
	}
}

func TestSettingsSecretEditIsMaskedUntilSaved(t *testing.T) {
	f := newSettingsFixture(t)
	f.activate(t, "auth.password")
	f.typeKeys("xyz")
	v := ansi.Strip(f.s.View(80, 24))
	if strings.Contains(v, "xyz") {
		t.Fatal("the typed secret was echoed")
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(f.calls) != 1 || f.calls[0] != [2]string{"auth.password", "xyz"} {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.s.dialog != nil {
		t.Fatal("a successful save must close the prompt")
	}
}

func TestSettingsEscapeCancelsWithoutSaving(t *testing.T) {
	f := newSettingsFixture(t)
	f.activate(t, "listen.bind")
	if f.s.dialog == nil {
		t.Fatal("the prompt did not open")
	}
	f.key(tea.KeyPressMsg{Code: tea.KeyEsc})
	if f.s.dialog != nil || len(f.calls) != 0 {
		t.Fatalf("dialog=%v calls=%v", f.s.dialog, f.calls)
	}
}

func TestSettingsInvalidValueStaysInThePrompt(t *testing.T) {
	f := newSettingsFixture(t)
	f.setErr = &cli.Error{Code: "CONFIG_INVALID", Message: "listen.port must be between 1 and 65535", Hint: "fix the value", Exit: cli.ExitUsage}
	f.activate(t, "listen.port")
	f.clearPrompt()
	f.typeKeys("8080")
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.dialog == nil || f.s.prompt == nil {
		t.Fatal("an invalid value must keep the prompt open")
	}
	if f.s.prompt.Error == nil {
		t.Fatal("the prompt must show an error")
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	v := ansi.Strip(f.s.View(80, 24))
	if !strings.Contains(v, "listen.port must be between 1 and 65535") || !strings.Contains(v, "fix the value") {
		t.Fatalf("the error and hint must be shown:\n%s", v)
	}
}

func TestSettingsPlainErrorIsNeverEchoed(t *testing.T) {
	f := newSettingsFixture(t)
	f.setErr = errors.New("dial tcp 10.0.0.1:1: secret-looking text")
	f.activate(t, "listen.port")
	f.clearPrompt()
	f.typeKeys("8080")
	f.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.s.prompt == nil || f.s.prompt.Error == nil {
		t.Fatal("the prompt must show an error")
	}
	if got := f.s.prompt.Error.Error(); got != "settings operation failed" {
		t.Fatalf("error = %q, want the fixed safe text", got)
	}
	if v := ansi.Strip(f.s.View(80, 24)); strings.Contains(v, "secret-looking") {
		t.Fatal("a plain error was echoed")
	}
}

func TestSettingsLoadErrorStillRenders(t *testing.T) {
	f := newSettingsFixture(t)
	f.s.load = func(string) (*config.Config, error) { return nil, errors.New("boom") }
	f.drain(f.s.loadCmd())
	f.selectGroup("listen")
	c := f.s.workspace.Card
	if len(c.Info) != 1 || c.Info[0] != "The configuration file could not be read." {
		t.Fatalf("info = %v", c.Info)
	}
	v := ansi.Strip(f.s.View(100, 30)) // two panes from 100 columns
	if !strings.Contains(v, "The configuration file could not be read.") {
		t.Fatalf("the load error must be shown:\n%s", v)
	}
}

func TestSettingsFrameContract(t *testing.T) {
	for _, gi := range []string{"listen", "auth", "fetch", "chain"} {
		f := newSettingsFixture(t)
		f.selectGroup(gi)
		for _, drilled := range []bool{false, true} {
			for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
				f.s.workspace.Drilled = drilled
				f.s.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				v := ansi.Strip(f.s.View(size[0], size[1]))
				lines := strings.Split(v, "\n")
				if len(lines) != size[1] {
					t.Fatalf("group=%s drilled=%v %dx%d: %d lines", gi, drilled, size[0], size[1], len(lines))
				}
				for _, l := range lines {
					if ansi.StringWidth(l) > size[0] {
						t.Fatalf("group=%s drilled=%v %dx%d: line wider than the screen: %q", gi, drilled, size[0], size[1], l)
					}
				}
			}
		}
	}
}
func TestSettingsOpenPromptFitsEverySize(t *testing.T) {
	f := newSettingsFixture(t)
	f.activate(t, "listen.port")
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		f.s.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v := ansi.Strip(f.s.View(size[0], size[1]))
		lines := strings.Split(v, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		if !strings.Contains(v, "Settings · listen.port") {
			t.Fatalf("%dx%d: prompt title missing:\n%s", size[0], size[1], v)
		}
	}
}

func TestSettingsProxyPromptSubmitsRawURLThroughUpdate(t *testing.T) {
	for _, tc := range []struct {
		key, raw string
	}{
		{"fetch.proxy", "http://fetch-user:fetch-pass@fetch.example:8080"},
		{"chain.pre_socks5", "socks5://chain-user:chain-pass@relay.example:1080"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			f := newSettingsFixture(t)
			f.activate(t, tc.key)
			f.typeKeys(tc.raw)
			f.key(tea.KeyPressMsg{Code: tea.KeyEnter})

			if len(f.calls) != 1 || f.calls[0][0] != tc.key || f.calls[0][1] != tc.raw {
				t.Errorf("%s: entered URL did not reach the save callback unchanged", tc.key)
			}
			if f.s.dialog != nil {
				t.Errorf("%s: successful save left the prompt open", tc.key)
			}
		})
	}
}

func TestSettingsProxyPromptCancelDoesNotSave(t *testing.T) {
	for _, tc := range []struct {
		key, raw string
	}{
		{"fetch.proxy", "http://fetch-user:fetch-pass@fetch.example:8080"},
		{"chain.pre_socks5", "socks5://chain-user:chain-pass@relay.example:1080"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			f := newSettingsFixture(t)
			f.activate(t, tc.key)
			f.typeKeys(tc.raw)
			f.key(tea.KeyPressMsg{Code: tea.KeyEsc})
			view := ansi.Strip(f.s.View(100, 24))
			if !strings.Contains(view, "Discard changes? Continue editing or discard") || !strings.Contains(view, "Continue") || !strings.Contains(view, "Discard") {
				t.Errorf("%s: changed prompt did not show the discard confirmation", tc.key)
			}
			f.key(tea.KeyPressMsg{Code: tea.KeyRight})
			f.key(tea.KeyPressMsg{Code: tea.KeyEnter})

			if len(f.calls) != 0 {
				t.Errorf("%s: cancel invoked the save callback", tc.key)
			}
			if f.s.dialog != nil {
				t.Errorf("%s: canceled prompt remains open", tc.key)
			}
		})
	}
}

func TestSettingsProxyPromptSaveErrorKeepsCredentialsOutOfUI(t *testing.T) {
	for _, tc := range []struct {
		key, raw, username, password string
	}{
		{"fetch.proxy", "http://fetch-user:fetch-pass@fetch.example:8080", "fetch-user", "fetch-pass"},
		{"chain.pre_socks5", "socks5://chain-user:chain-pass@relay.example:1080", "chain-user", "chain-pass"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			f := newSettingsFixture(t)
			f.setErr = errors.New("save failed for " + tc.raw)
			f.activate(t, tc.key)
			f.typeKeys(tc.raw)
			f.key(tea.KeyPressMsg{Code: tea.KeyEnter})

			if len(f.calls) != 1 || f.calls[0][0] != tc.key || f.calls[0][1] != tc.raw {
				t.Errorf("%s: entered URL did not reach the save callback unchanged", tc.key)
			}
			if f.s.dialog == nil || f.s.prompt == nil || f.s.prompt.Saving {
				t.Errorf("%s: save error did not leave an editable prompt open", tc.key)
			}
			if f.s.prompt != nil && (f.s.prompt.Error == nil || f.s.prompt.Error.Error() != "settings operation failed") {
				t.Errorf("%s: save error was not replaced with the safe message", tc.key)
			}

			view := ansi.Strip(f.s.View(100, 30))
			cardLine, _ := cardLine(f.s.workspace.Card, tc.key)
			for _, text := range []string{tc.raw, tc.username, tc.password} {
				if strings.Contains(view, text) || strings.Contains(cardLine.Value, text) {
					t.Errorf("%s: save error exposed credential text in the UI", tc.key)
				}
				if f.s.prompt != nil && f.s.prompt.Error != nil && strings.Contains(f.s.prompt.Error.Error(), text) {
					t.Errorf("%s: save error exposed credential text in the prompt", tc.key)
				}
			}
		})
	}
}

func TestSettingsParityRefs(t *testing.T) {
	f := newSettingsFixture(t)
	got := map[string]bool{}
	for _, r := range f.s.ParityRefs() {
		if got[r] {
			t.Errorf("duplicate ref %s", r)
		}
		got[r] = true
	}
	want := map[string]bool{"settings/list": true}
	for _, k := range settingsKeys {
		want["settings/"+k] = true
	}
	for r := range got {
		if !want[r] {
			t.Errorf("unexpected ref %s", r)
		}
	}
	for r := range want {
		if !got[r] {
			t.Errorf("missing %s in %v", r, f.s.ParityRefs())
		}
	}
	if len(got) != 11 {
		t.Errorf("refs = %d, want 11", len(got))
	}
}
