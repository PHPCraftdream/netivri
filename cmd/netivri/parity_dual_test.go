package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// Check 6 of docs/design/parity.md: the same effect. Every capability that
// changes stored state and exists on both sides has a scenario that runs the
// CLI command on one home and the shell walk (real keys) on an identical
// second home, then compares what was stored. Scenarios register themselves
// under the capability id of parity_catalog_test.go in an init() of their own
// file (parity_dual_<area>_test.go).

// dualScenarios maps a capability id to its dual-run scenario.
var dualScenarios = map[string]func(t *testing.T){}

// dualNoScenario lists the capabilities that have both sides but need no
// dual run, each with the reason: they change nothing that is stored, or the
// effect is a running process and not a file.
var dualNoScenario = map[string]string{
	"provider.list":      "read-only",
	"provider.preview":   "read-only: stores nothing",
	"provider.countries": "read-only on the CLI; the shell picker stores the account choice through account.country",
	"nodes.list":         "read-only",
	"account.list":       "read-only",
	"account.show":       "read-only",
	"server.list":        "read-only",
	"server.link":        "read-only: derives a client link from the stored server",
	"service.up":         "starts a process; its stored effect is none",
	"service.up-flags":   "the flags apply to the started process; the stored listen settings are compared by settings.global",
	"service.down":       "stops a process; its stored effect is none",
	"service.status":     "read-only",
	"service.doctor":     "read-only",
	"service.logs":       "read-only",
	"service.stats":      "read-only",
	"service.diag":       "read-only dial diagnostics",
}

// dualHomes builds two homes with the same seed: one for the CLI run and one
// for the shell run.
func dualHomes(t *testing.T, seed func(t *testing.T) string) (cliHome, shellHome string) {
	t.Helper()
	return seed(t), seed(t)
}

// dualCLI runs one CLI command on a home and fails on an error. --home goes
// after the command: dispatch looks the command up at args[0].
func dualCLI(t *testing.T, home string, args ...string) {
	t.Helper()
	mustDispatch(t, append(append([]string{}, args...), "--home", home)...)
}

// dualShell drives one shell screen with real key presses. Background work
// runs inline, a confirmation the screen opens is held until Confirm.
type dualShell struct {
	t      *testing.T
	screen tui.Screen
	modal  tui.Modal
}

// newDualShell builds the tab with the given title on a home and loads it.
func newDualShell(t *testing.T, home, tab string) *dualShell {
	t.Helper()
	d := &dualShell{t: t}
	env := tui.Env{Home: home, Background: func(_ string, w func() tea.Msg) tea.Cmd { return w }}
	env.OpenModal = func(m tui.Modal) tea.Cmd {
		d.modal = m
		return nil
	}
	for _, x := range interactiveOptions(nil, home, 0).Tabs {
		if x.Title == tab {
			d.screen = x.New(env)
			d.screen = parityDrain(d.screen, d.screen.Init())
			return d
		}
	}
	t.Fatalf("no tab %q", tab)
	return nil
}

var dualKeyNames = map[string]tea.KeyPressMsg{
	"enter":     {Code: tea.KeyEnter},
	"esc":       {Code: tea.KeyEscape},
	"tab":       {Code: tea.KeyTab},
	"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
	"up":        {Code: tea.KeyUp},
	"down":      {Code: tea.KeyDown},
	"left":      {Code: tea.KeyLeft},
	"right":     {Code: tea.KeyRight},
	"pgup":      {Code: tea.KeyPgUp},
	"pgdown":    {Code: tea.KeyPgDown},
	"home":      {Code: tea.KeyHome},
	"end":       {Code: tea.KeyEnd},
	"space":     {Code: tea.KeySpace, Text: " "},
	"backspace": {Code: tea.KeyBackspace},
	"ctrl+u":    {Code: 'u', Mod: tea.ModCtrl},
}

// Keys presses named keys ("enter", "tab", "ctrl+u", ...) or a single
// character ("a", "/"), draining the background work after each press.
func (d *dualShell) Keys(keys ...string) {
	d.t.Helper()
	for _, k := range keys {
		msg, ok := dualKeyNames[k]
		if !ok {
			r := []rune(k)
			if len(r) != 1 {
				d.t.Fatalf("unknown key %q", k)
			}
			msg = tea.KeyPressMsg{Code: r[0], Text: k}
		}
		var c tea.Cmd
		d.screen, c = d.screen.Update(msg)
		d.screen = parityDrain(d.screen, c)
	}
}

// Type enters text character by character.
func (d *dualShell) Type(text string) {
	d.t.Helper()
	for _, r := range text {
		d.Keys(string(r))
	}
}

// Confirm answers the confirmation the screen opened.
func (d *dualShell) Confirm(yes bool) {
	d.t.Helper()
	if d.modal == nil {
		d.t.Fatal("the screen opened no confirmation")
	}
	d.modal = nil
	var c tea.Cmd
	d.screen, c = d.screen.Update(tui.ConfirmResultMsg{Confirmed: yes, Canceled: !yes})
	d.screen = parityDrain(d.screen, c)
}

// dualVolatile blanks the parts of a stored file that differ between two runs
// by nature (generated keys, timestamps). Each scenario passes its own.
type dualVolatile = *regexp.Regexp

// dualState lists every stored file of a home that holds configuration or
// subscription snapshots, with volatile parts blanked.
func dualState(t *testing.T, home string, volatile ...dualVolatile) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, re := range volatile {
			s = re.ReplaceAllString(s, "<volatile>")
		}
		rel, _ := filepath.Rel(home, path)
		out[filepath.ToSlash(rel)] = s
	}
	add(config.ConfigPath(home))
	if entries, err := os.ReadDir(filepath.Join(home, "subscriptions")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				add(filepath.Join(home, "subscriptions", e.Name()))
			}
		}
	}
	return out
}

// dualHash shortens a value to a fingerprint, so a mismatch report never
// prints a secret.
func dualHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}

// dualEqual fails when the two homes stored different state. The report names
// the file and line and fingerprints values; it never prints them.
func dualEqual(t *testing.T, cliHome, shellHome string, volatile ...dualVolatile) {
	t.Helper()
	a, b := dualState(t, cliHome, volatile...), dualState(t, shellHome, volatile...)
	names := map[string]bool{}
	for n := range a {
		names[n] = true
	}
	for n := range b {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		x, okx := a[n]
		y, oky := b[n]
		if okx != oky {
			t.Errorf("%s: stored by the CLI=%v, by the shell=%v", n, okx, oky)
			continue
		}
		if x == y {
			continue
		}
		xl, yl := strings.Split(x, "\n"), strings.Split(y, "\n")
		for i := 0; i < len(xl) || i < len(yl); i++ {
			var l1, l2 string
			if i < len(xl) {
				l1 = xl[i]
			}
			if i < len(yl) {
				l2 = yl[i]
			}
			if l1 != l2 {
				t.Errorf("%s line %d differs: CLI %s, shell %s", n, i+1, dualHash(l1), dualHash(l2))
			}
		}
	}
}

// TestParityDualRunCoversEveryCapability keeps the registry honest: a
// capability with both sides has a dual scenario or a reason not to, and
// nothing names a capability that does not exist.
func TestParityDualRunCoversEveryCapability(t *testing.T) {
	ids := map[string]bool{}
	for _, c := range parityCatalog {
		ids[c.ID] = true
		if len(c.CLI) == 0 || len(c.TUI) == 0 {
			continue
		}
		_, has := dualScenarios[c.ID]
		_, skip := dualNoScenario[c.ID]
		switch {
		case has && skip:
			t.Errorf("%s has a dual scenario and is listed as needing none", c.ID)
		case !has && !skip:
			t.Errorf("%s changes state on both sides and has no dual-run scenario (add one in parity_dual_*_test.go, or a reason to dualNoScenario)", c.ID)
		}
	}
	for id := range dualScenarios {
		if !ids[id] {
			t.Errorf("dual scenario %q names no capability", id)
		}
	}
	for id := range dualNoScenario {
		if !ids[id] {
			t.Errorf("dualNoScenario names no capability %q", id)
		}
	}
}

// TestParityDualRun runs every registered scenario.
func TestParityDualRun(t *testing.T) {
	names := make([]string, 0, len(dualScenarios))
	for id := range dualScenarios {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		t.Run(id, dualScenarios[id])
	}
}
