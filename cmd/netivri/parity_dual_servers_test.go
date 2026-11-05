package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

// Parity check 6 (docs/design/parity.md, Checks item 6) for the server
// capabilities: the CLI command runs on one home, the Servers-screen walk
// (real keys through newDualShell) on an identical second home, and
// dualEqual compares what was stored.
//
// Volatile parts, read from the storage code: server secrets — generated
// passwords, Reality keys, TLS certificate material — never reach
// config.ktav. config.Server.Params holds non-secret protocol options only
// ("Params contains protocol options, not credentials; server secrets are
// stored separately", internal/config/servers.go), and the generated
// material lives in <home>/server-secrets.ktav, which dualState does not
// read. config.ktav carries no timestamps and no home paths, and these
// scenarios add no subscription snapshots, so no volatile regexp is needed:
// the stored files must be byte-identical.

func init() {
	dualScenarios["server.add"] = testDualServerAdd
	dualScenarios["server.rotate"] = testDualServerRotate
	dualScenarios["server.remove"] = testDualServerRemove
	dualScenarios["server.update"] = testDualServerUpdate
}

// dualServersSeed builds a deterministic home: accounts alice and bob, a
// shadowsocks and a vless-reality server, secrets materialized so rotate
// and update have a store to work on (serversFixture's pattern). Two calls
// produce identical config.ktav; the t.TempDir path is never stored.
func dualServersSeed(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	c := config.Default()
	c.Accounts = []config.Account{
		{Login: "alice", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"},
		{Login: "bob", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash"},
	}
	c.Servers = []config.Server{
		{Name: "ss", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 1234, Accounts: "alice", PublicHost: "pub.test", Params: map[string]string{"cipher": "aes-128-gcm"}},
		{Name: "vless", Protocol: "vless-reality", Bind: "127.0.0.1", Port: 2345, Accounts: "alice", Params: map[string]string{"sni": "reality-sni.example"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("invalid seed: %v", err)
	}
	if err := config.Save(config.ConfigPath(home), c); err != nil {
		t.Fatal(err)
	}
	specs, err := c.ResolveServers()
	if err != nil {
		t.Fatalf("resolve seed servers: %v", err)
	}
	for _, spec := range specs {
		if _, err := servers.NewStore(home).EnsureSecrets(spec); err != nil {
			t.Fatalf("materialize secrets for %s: %v", spec.Name, err)
		}
	}
	return home
}

// dualServersResize sizes the workspace to the wide layout the real frame
// runs at, so Tab can focus the card pane.
func dualServersResize(d *dualShell, width, height int) {
	var c tea.Cmd
	d.screen, c = d.screen.Update(tea.WindowSizeMsg{Width: width, Height: height})
	d.screen = parityDrain(d.screen, c)
}

// dualServersView renders the screen; prompts initialize lazily on View.
func dualServersView(d *dualShell, width, height int) string {
	return d.screen.View(width, height)
}

// dualServersWizardProtocol picks the protocol in the wizard's first step
// (ChoicePrompt over ServerProtocols() order) and advances with Enter; the
// servers dialog rebuilds the wizard for the new protocol and lands on the
// Name step (tui_servers_wizard.go updateWizard).
func dualServersWizardProtocol(t *testing.T, d *dualShell, name string) {
	t.Helper()
	dial := d.screen.(*serversScreen).dialog
	if dial == nil || dial.wizard == nil {
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
	_ = dualServersView(d, 120, 30)
	p := dial.wizard.Steps[0].Prompt
	cur := 0
	for i, o := range p.Options {
		if o.Value == p.Value {
			cur = i
		}
	}
	for i := 0; i < (idx-cur+len(p.Options))%len(p.Options); i++ {
		d.Keys("down")
	}
	d.Keys("enter")
	rebuilt := d.screen.(*serversScreen).dialog
	if rebuilt == nil || rebuilt.wizard == nil || rebuilt.wizard.Values["Protocol"] != name || rebuilt.wizard.Index != 1 {
		t.Fatalf("protocol %q did not commit onto the Name step", name)
	}
}

// dualServersAnswerText replaces the whole prefilled answer of the open
// field prompt (prefill length clear) with value and saves with Enter. The
// tui_servers.go flow: Enter on the focused card line opens the prompt, the
// save lands as serversDialogResult and runs ServerUpdate; a failed update
// keeps the prompt open with the error.
func dualServersAnswerText(t *testing.T, d *dualShell, value string, clear int) {
	t.Helper()
	s := d.screen.(*serversScreen)
	if s.dialog == nil || s.dialog.prompt == nil {
		t.Fatal("field prompt did not open")
	}
	if len(s.dialog.prompt.Value) != clear {
		t.Fatalf("prompt %q prefilled with %q, want %d characters", s.dialog.prompt.TitleText, s.dialog.prompt.Value, clear)
	}
	for i := 0; i < clear; i++ {
		d.Keys("backspace")
	}
	d.Type(value)
	d.Keys("enter")
	if s.dialog != nil {
		text := "a dialog is still open"
		if s.dialog.prompt != nil {
			text = fmt.Sprintf("prompt %q still open, error: %v", s.dialog.prompt.TitleText, s.dialog.prompt.Error)
		}
		t.Fatalf("the edit did not complete: %s", text)
	}
}

// server.add: the documented flags versus the add wizard (hotkey a) on the
// Servers screen. The wizard keeps the cipher and UDP-relay defaults, which
// the CLI passes explicitly (serverAddOptionsFromWizard stores every
// answered param step); --default has no wizard step and is not exercised.
func testDualServerAdd(t *testing.T) {
	cliHome, shellHome := dualHomes(t, dualServersSeed)
	dualCLI(t, cliHome, "server", "add", "--protocol", "shadowsocks", "--name", "wss01",
		"--account", "alice", "--port", "45701", "--bind", "0.0.0.0",
		"--public-host", "link.test",
		"--param", "cipher=2022-blake3-aes-128-gcm", "--param", "udp=false")

	d := newDualShell(t, shellHome, "Servers")
	dualServersResize(d, 120, 30)
	d.Keys("a") // Add a server... opens the wizard
	dualServersWizardProtocol(t, d, "shadowsocks")
	d.Type("wss01")
	d.Keys("enter") // Name -> Bind
	d.Keys("enter") // Bind keeps 0.0.0.0 -> Port
	d.Type("45701")
	d.Keys("enter") // Port -> Public host
	d.Type("link.test")
	d.Keys("enter") // Public host -> Cipher (keeps the default)
	d.Keys("enter") // Cipher -> UDP relay (keeps false)
	d.Keys("enter") // UDP relay -> Accounts (commits alice)
	d.Keys("enter") // Accounts -> review
	d.Keys("enter") // review [Create] runs the add through the screen
	d.Keys("esc")   // close the "Server added" modal
	dualEqual(t, cliHome, shellHome)
}

// server.rotate: `server rotate ss` versus the Rotate keys... card action
// with its confirmation. Rotation touches only server-secrets.ktav, which is
// random per home: each side must replace it (the file differs from before),
// and both config.ktav files must stay identical.
func testDualServerRotate(t *testing.T) {
	cliHome, shellHome := dualHomes(t, dualServersSeed)
	secrets := func(home string) string {
		b, err := os.ReadFile(filepath.Join(home, "server-secrets.ktav"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cliBefore, shellBefore := secrets(cliHome), secrets(shellHome)
	dualCLI(t, cliHome, "server", "rotate", "ss")

	d := newDualShell(t, shellHome, "Servers")
	dualServersResize(d, 120, 30)
	d.Keys("enter") // load the ss detail on the list pane
	d.Keys("tab")   // focus the card
	d.Keys("t")     // Rotate keys... -> confirmation through env.OpenModal
	d.Confirm(true)
	if secrets(cliHome) == cliBefore {
		t.Fatal("the CLI rotation did not replace the server secrets")
	}
	if secrets(shellHome) == shellBefore {
		t.Fatal("the shell rotation did not replace the server secrets")
	}
	dualEqual(t, cliHome, shellHome)
}

// server.remove: `server remove ss` versus Remove... with its confirmation;
// the vless entry must survive on both sides.
func testDualServerRemove(t *testing.T) {
	cliHome, shellHome := dualHomes(t, dualServersSeed)
	dualCLI(t, cliHome, "server", "remove", "ss")

	d := newDualShell(t, shellHome, "Servers")
	dualServersResize(d, 120, 30)
	d.Keys("enter") // load the ss detail on the list pane
	d.Keys("tab")   // focus the card
	d.Keys("d")     // Remove... -> confirmation
	d.Confirm(true)
	dualEqual(t, cliHome, shellHome)
}

// server.update: four changeable fields in one CLI command versus the
// Bind/Port/Public host/Accounts card lines, one prompt each (the card cursor
// stays on the edited line, so one down moves to the next field). Accounts
// keeps exactly one login: shadowsocks is single-user
// (config.validateServers). --param is not exercised: the only shadowsocks
// params are the cipher, whose credential has a size per cipher and is kept
// when only the cipher changes (the same error on both sides), and udp, which
// accepts only false; server.add covers --param.
func testDualServerUpdate(t *testing.T) {
	cliHome, shellHome := dualHomes(t, dualServersSeed)
	dualCLI(t, cliHome, "server", "update", "ss",
		"--bind", "0.0.0.0", "--port", "1235", "--public-host", "new.test",
		"--account", "bob")

	d := newDualShell(t, shellHome, "Servers")
	dualServersResize(d, 120, 30)
	d.Keys("tab") // card focus; the cursor sits on Bind

	d.Keys("enter") // Bind, prefilled 127.0.0.1
	dualServersAnswerText(t, d, "0.0.0.0", len("127.0.0.1"))
	d.Keys("down")
	d.Keys("enter") // Port, prefilled 1234
	dualServersAnswerText(t, d, "1235", len("1234"))
	d.Keys("down")
	d.Keys("enter") // Public host, prefilled pub.test
	dualServersAnswerText(t, d, "new.test", len("pub.test"))
	d.Keys("down")
	d.Keys("enter") // Accounts, prefilled alice
	dualServersAnswerText(t, d, "bob", len("alice"))
	dualEqual(t, cliHome, shellHome)
}
