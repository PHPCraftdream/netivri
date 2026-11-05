package main

import "testing"

// Dual-run scenarios for the account capabilities (check 6 of
// docs/design/parity.md): the CLI command on one home, the shell walk (real
// keys) on an identical second home, then the stored state is compared.
//
// Nothing an account command stores is random or time-based: the password of
// a new or cloned account is the "init" placeholder on both sides and no
// account command touches the subscription snapshots, so no volatile regexp
// is needed here.
//
// The account card (refreshSelectedCard in tui_accounts.go) lists its lines
// in this order: provider, countries, exit, clients, pre, clear-pre, post,
// clear-post, password, clone, remove, route, bulk. clear-pre and clear-post
// are enabled only while the account owns that hop (a URL, not "none"; the
// resolved source of "none" is "none", not "account"), bulk is enabled only
// while accounts are marked, and the password line is read-only. Every
// mutation rebuilds the card and selects the provider line again, so each
// walk below counts its "down" presses over the enabled lines of the current
// layout. The dialog a card line or hotkey opens is reached with plain keys;
// the remove and bulk confirmations arrive through env.OpenModal and are
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

// account.hops: pre-hop and post-hop set through the card fields, pre cleared
// with the Clear pre-hop action while it is enabled, the "none" value through
// the field, post cleared with Clear post-hop. The down counts follow the
// card layout documented at the top of the file.
func dualAccountScenarioHops(t *testing.T) {
	cliHome, shellHome := dualHomes(t, switchHome)
	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--pre", "socks5://pre.example:1080")
	d := newDualShell(t, shellHome, "Accounts")
	d.Keys("enter")                        // drill into alice
	d.Keys("down", "down", "down", "down") // provider, countries, exit, clients -> pre-hop
	d.Keys("enter")
	d.Type("socks5://pre.example:1080")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	// alice owns her pre-hop now, so Clear pre-hop is enabled: five downs.
	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--pre", "inherit")
	d.Keys("down", "down", "down", "down", "down") // -> clear pre-hop
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--pre", "none")
	d.Keys("down", "down", "down", "down") // -> pre-hop (clear-pre disabled again)
	d.Keys("enter")
	d.Type("none")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--post", "http://post.example:8080")
	// clear-pre and clear-post are both disabled: five downs pass pre-hop and
	// land on post-hop.
	d.Keys("down", "down", "down", "down", "down")
	d.Keys("enter")
	d.Type("http://post.example:8080")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	// alice owns her post-hop now, so Clear post-hop is enabled; clear-pre is
	// still disabled: six downs pass pre-hop and post-hop.
	dualCLI(t, cliHome, "account", "set-socks5", "alice", "--post", "inherit")
	d.Keys("down", "down", "down", "down", "down", "down") // -> clear post-hop
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
