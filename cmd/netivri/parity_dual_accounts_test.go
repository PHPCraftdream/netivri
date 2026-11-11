package main

import (
	"testing"

	"github.com/PHPCraftdream/netivri/internal/tui"
)

// Dual-run scenarios for the account capabilities (check 6 of
// docs/design/parity.md): the CLI command on one home, the shell walk (real
// keys) on an identical second home, then the stored state is compared.
//
// Nothing an account command stores is random or time-based: the password of
// a new or cloned account is the "init" placeholder on both sides and no
// account command touches the subscription snapshots, so no volatile regexp
// is needed here.
//
// The account card (refreshSelectedCard in tui_accounts.go) orders fields and
// actions, but card movement skips disabled and read-only rows. The hop parity
// scenario therefore selects by line ID using real movement keys. Hop fields
// open an operation picker; URL choices open a separate blank secret prompt.
// The remove and bulk confirmations arrive through env.OpenModal and are
// answered with Confirm.

func init() {
	dualScenarios["account.add"] = dualAccountScenarioAdd
	dualScenarios["account.clone"] = dualAccountScenarioClone
	dualScenarios["account.exit"] = dualAccountScenarioExit
	dualScenarios["account.country"] = dualAccountScenarioCountry
	dualScenarios["account.provider"] = dualAccountScenarioProvider
	dualScenarios["account.clients"] = dualAccountScenarioClients
	dualScenarios["account.hops"] = dualAccountScenarioHops
	dualScenarios["account.bulk"] = dualAccountScenarioBulk
	dualScenarios["account.remove"] = dualAccountScenarioRemove
}

// dualAccountAt selects the list row at the given zero-based position in
// config order: "home" jumps to the first row, then "down" walks on. Call it
// with no dialog open and the list focused.
func dualAccountAt(d *dualShell, pos int) {
	d.t.Helper()
	d.Keys("home")
	for i := 0; i < pos; i++ {
		d.Keys("down")
	}
}

// account card selection follows real movement, which skips disabled and
// read-only rows. Resolve the target on each walk rather than assuming that
// the physical line offset is the number of key presses.
func dualSelectAccountCardLine(t *testing.T, d *dualShell, id string) {
	t.Helper()
	s, ok := d.screen.(*accountsScreen)
	if !ok || s.workspace.Card == nil {
		t.Fatal("account card is unavailable")
	}
	if s.width >= 100 && s.workspace.Focus() != tui.PaneCard {
		d.Keys("tab")
	} else if s.width < 100 && !s.workspace.Drilled {
		d.Keys("enter")
	}
	dualSelectCardLine(t, d, s.workspace.Card, id)
}

// dualChooseAccountHopOperation makes the operation choice with real key
// presses. The subsequent URL entry belongs to the blank SecretPrompt, not
// the ChoicePrompt.
func dualChooseAccountHopOperation(t *testing.T, d *dualShell, value string) {
	t.Helper()
	s, ok := d.screen.(*accountsScreen)
	if !ok || s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.ChoicePrompt {
		t.Fatal("account hop operation picker is not open")
	}
	choice := -1
	for i, option := range s.dialog.prompt.Options {
		if option.Value == value {
			choice = i
			break
		}
	}
	if choice < 0 {
		t.Fatalf("account hop picker has no operation %q", value)
	}
	for i := 0; i < len(s.dialog.prompt.Options); i++ {
		selected, err := s.dialog.prompt.GetAnswer()
		if err != nil {
			t.Fatalf("read account hop picker choice: %v", err)
		}
		if selected == value {
			break
		}
		d.Keys("down")
	}
	selected, err := s.dialog.prompt.GetAnswer()
	if err != nil || selected != value {
		t.Fatalf("account hop picker walk did not reach operation %q", value)
	}
	d.Keys("enter")
	if (value == "replace" || value == "add") && (s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.SecretPrompt || s.dialog.prompt.Value != "") {
		t.Fatal("account hop operation did not open a blank secret prompt")
	}
}

// account.add: the add wizard (Login, Provider, Country, Exit, Clients,
// Custom clients, Pre-hop, Post-hop, review) against `account add` with the
// same values. The provider picker starts on "any", one "down" reaches p1;
// the country prompt is prefilled with "any" and cleared first; the clients
// picker starts on "everyone", one "down" reaches loopback; the exit picker
// already shows route.
func dualAccountScenarioAdd(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "add", "dave",
		"--provider", "p1", "--country", "DE", "--clients", "loopback",
		"--exit", "route",
		"--pre-socks5", "socks5://hop.example:1080",
		"--post-socks5", "http://hop2.example:8080")
	d := newDualShell(t, shellHome, "Accounts")
	d.Keys("a") // the + Add wizard
	d.Type("dave")
	d.Keys("enter")  // to Provider
	d.Keys("down")   // any -> p1
	d.Keys("enter")  // to Country
	d.Keys("ctrl+u") // clear the prefilled "any"
	d.Type("DE")
	d.Keys("enter") // to Exit (route stays selected)
	d.Keys("enter") // to Clients
	d.Keys("down")  // everyone -> loopback
	d.Keys("enter") // to Custom clients (unused, stays empty)
	d.Keys("enter") // to Pre-hop
	d.Type("socks5://hop.example:1080")
	d.Keys("enter") // to Post-hop
	d.Type("http://hop2.example:8080")
	d.Keys("enter") // to the review step
	d.Keys("enter") // Create
	dualEqual(t, cliHome, shellHome)
}

// account.clone: the clone prompt proposes login-2 and copies provider,
// countries, exit, clients and hops. The shell never copies a password (the
// clone gets the "init" placeholder, as with --password init); the CLI copies
// the source's hash by default, so the second step compares the shell with
// --password init, the CLI form that does what the shell does. The
// --provider/--country/--clients/--exit overrides of the CLI are a clone
// followed by the single edits, which account.provider, account.country,
// account.clients and account.exit compare.
func dualAccountScenarioClone(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "clone", "alice", "alice-2")
	d := newDualShell(t, shellHome, "Accounts")
	d.Keys("c")     // clone the selected alice; "alice-2" is prefilled
	d.Keys("enter") // accept it
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "clone", "bob", "bob-2", "--password", "init")
	dualAccountAt(d, 1) // bob
	d.Keys("c")
	d.Keys("enter") // "bob-2" is prefilled
	dualEqual(t, cliHome, shellHome)
}

// account.exit: carol (no provider, country or hop) goes direct through the
// exit card line, alice (route by default) gets an explicit route.
func dualAccountScenarioExit(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "set-exit", "carol", "direct")
	d := newDualShell(t, shellHome, "Accounts")
	dualAccountAt(d, 2)    // carol
	d.Keys("enter")        // drill into her card
	d.Keys("down", "down") // provider -> countries -> exit
	d.Keys("enter")        // edit exit; route is selected for an empty value
	d.Keys("down")         // route -> direct
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-exit", "alice", "route")
	d.Keys("esc")       // back to the list
	dualAccountAt(d, 0) // alice
	d.Keys("enter")     // drill
	d.Keys("down", "down")
	d.Keys("enter") // edit exit (route stays selected)
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
}

// account.country: alice's countries become the list NL,DE, bob's are cleared
// with --none / an emptied prompt.
func dualAccountScenarioCountry(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "set-country", "alice", "NL,DE")
	d := newDualShell(t, shellHome, "Accounts")
	d.Keys("enter") // drill into alice's card
	d.Keys("down")  // provider -> countries
	d.Keys("enter") // edit countries, prefilled DE
	d.Keys("ctrl+u")
	d.Type("NL,DE")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-country", "bob", "--none")
	d.Keys("esc")
	dualAccountAt(d, 1) // bob
	d.Keys("enter")     // drill
	d.Keys("down")      // countries
	d.Keys("enter")     // prefilled NL
	d.Keys("ctrl+u")
	d.Keys("enter") // empty clears
	dualEqual(t, cliHome, shellHome)
}

// account.provider: carol (no provider) gets p1, alice's provider is cleared
// with --none / an emptied prompt.
func dualAccountScenarioProvider(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "set-provider", "carol", "p1")
	d := newDualShell(t, shellHome, "Accounts")
	dualAccountAt(d, 2) // carol
	d.Keys("enter")     // drill; the provider line is selected
	d.Keys("enter")     // edit provider, empty on carol
	d.Type("p1")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-provider", "alice", "--none")
	d.Keys("esc")
	dualAccountAt(d, 0) // alice
	d.Keys("enter")     // drill
	d.Keys("enter")     // provider line, prefilled p1
	d.Keys("ctrl+u")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
}

// account.clients: carol gets a CIDR list, bob loopback, carol is cleared
// again with --none.
func dualAccountScenarioClients(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "set-clients", "carol", "10.0.0.0/8")
	d := newDualShell(t, shellHome, "Accounts")
	dualAccountAt(d, 2)            // carol
	d.Keys("enter")                // drill
	d.Keys("down", "down", "down") // provider, countries, exit -> clients
	d.Keys("enter")                // edit clients, empty on carol
	d.Type("10.0.0.0/8")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-clients", "bob", "loopback")
	d.Keys("esc")
	dualAccountAt(d, 1)            // bob
	d.Keys("enter")                // drill
	d.Keys("down", "down", "down") // -> clients
	d.Keys("enter")                // empty on bob
	d.Type("loopback")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-clients", "carol", "--none")
	d.Keys("esc")
	dualAccountAt(d, 2)            // carol
	d.Keys("enter")                // drill
	d.Keys("down", "down", "down") // -> clients
	d.Keys("enter")                // prefilled 10.0.0.0/8
	d.Keys("ctrl+u")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
}

// account.hops: card fields open an operation picker; URL operations then
// open a blank secret prompt. Clear actions inherit from the next scope.
func dualAccountScenarioHops(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	d := newDualShell(t, shellHome, "Accounts")

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--pre", "socks5://pre.example:1080")
	dualSelectAccountCardLine(t, d, "pre")
	d.Keys("enter")
	dualChooseAccountHopOperation(t, d, "replace")
	d.Type("socks5://pre.example:1080")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--pre", "inherit")
	dualSelectAccountCardLine(t, d, "clear-pre")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--pre", "none")
	dualSelectAccountCardLine(t, d, "pre")
	d.Keys("enter")
	dualChooseAccountHopOperation(t, d, "none")
	dualEqual(t, cliHome, shellHome)

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--pre", "inherit")
	dualSelectAccountCardLine(t, d, "pre")
	d.Keys("enter")
	dualChooseAccountHopOperation(t, d, "inherit")
	dualEqual(t, cliHome, shellHome)

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--post", "http://post.example:8080")
	dualSelectAccountCardLine(t, d, "post")
	d.Keys("enter")
	dualChooseAccountHopOperation(t, d, "replace")
	d.Type("http://post.example:8080")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--post", "inherit")
	dualSelectAccountCardLine(t, d, "clear-post")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)
}

// account.bulk: mark all three accounts with A, then apply countries,
// providers, allowed clients and the exit mode to the marks, the shell side
// of set-country/set-provider/set-clients/set-exit --all.
func dualAccountScenarioBulk(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	d := newDualShell(t, shellHome, "Accounts")
	d.Keys("A") // mark all three
	dualCLI(t, cliHome, "account", "set-country", "--all", "DE")
	d.Keys("b")    // the field picker, provider selected
	d.Keys("down") // provider -> countries
	d.Keys("enter")
	d.Type("DE")
	d.Keys("enter")
	d.Confirm(true)
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-provider", "--all", "p1,p2")
	d.Keys("b") // provider stays selected
	d.Keys("enter")
	d.Type("p1,p2")
	d.Keys("enter")
	d.Confirm(true)
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-clients", "--all", "loopback")
	d.Keys("b")
	d.Keys("down", "down", "down") // provider, countries, exit -> allowed clients
	d.Keys("enter")
	d.Type("loopback")
	d.Keys("enter")
	d.Confirm(true)
	dualEqual(t, cliHome, shellHome)
	dualCLI(t, cliHome, "account", "set-exit", "--all", "route")
	d.Keys("b")
	d.Keys("down", "down") // provider, countries -> exit
	d.Keys("enter")        // the value picker already shows route
	d.Keys("enter")
	d.Confirm(true)
	dualEqual(t, cliHome, shellHome)

	// A subset: the comma list of the CLI against Space marks.
	d.Keys("M") // clear the marks
	dualAccountAt(d, 0)
	d.Keys("space", "down", "space") // mark alice and bob
	dualCLI(t, cliHome, "account", "set-country", "alice,bob", "NL")
	d.Keys("b")
	d.Keys("down") // provider -> countries
	d.Keys("enter")
	d.Type("NL")
	d.Keys("enter")
	d.Confirm(true)
	dualEqual(t, cliHome, shellHome)
}

// account.remove: carol is removed through the d hotkey and its confirmation.
func dualAccountScenarioRemove(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "rm", "carol")
	d := newDualShell(t, shellHome, "Accounts")
	dualAccountAt(d, 2) // carol
	d.Keys("d")         // the screen opens the remove confirmation
	d.Confirm(true)
	dualEqual(t, cliHome, shellHome)
}
