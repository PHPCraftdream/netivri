package main

// Check 6 of docs/design/parity.md for the Providers capabilities and the
// global settings: each scenario runs the CLI command on one home and the
// shell walk (real keys) on an identical second home, then dualEqual
// compares config.ktav and the stored snapshots. Only what is random or
// time-based by nature is blanked with a volatile regexp (fetch timestamps,
// argon2 salts).

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func init() {
	dualScenarios["provider.add"] = dualProviderAddScenario
	dualScenarios["provider.hops"] = dualProviderHopsScenario
	dualScenarios["provider.refresh"] = dualProviderRefreshScenario
	dualScenarios["provider.remove"] = dualProviderRemoveScenario
	dualScenarios["provider.rename"] = dualProviderRenameScenario
	dualScenarios["provider.url"] = dualProviderURLScenario
	dualScenarios["provider.via"] = dualProviderViaScenario
	dualScenarios["settings.global"] = dualSettingsGlobalScenario
}

// dualRunCLI runs one CLI command through dispatch on a state directory.
// Unlike dualCLI it places --home after the subcommand: dispatch resolves
// commands[args[0]] before any global-flag parsing, and runSub/runConfig
// switch on their args[0] before parsing flags, so a leading --home fails
// with "unknown command".
func dualRunCLI(t *testing.T, home string, args ...string) {
	t.Helper()
	if len(args) < 2 {
		t.Fatalf("dualRunCLI needs a command and a subcommand, got %v", args)
	}
	argv := make([]string, 0, len(args)+2)
	argv = append(argv, args[0], args[1], "--home", home)
	argv = append(argv, args[2:]...)
	mustDispatch(t, argv...)
}

// dualUseFakeFetch installs the same fake subscription body on both sides:
// subFetch serves the CLI (sub add, sub update) and providersFetch serves
// the Providers screen built by newDualShell.
func dualUseFakeFetch(t *testing.T, body string) {
	t.Helper()
	f := fakeBody(body)
	oldSub, oldProviders := subFetch, providersFetch
	subFetch, providersFetch = f, f
	t.Cleanup(func() { subFetch, providersFetch = oldSub, oldProviders })
}

// dualVolatileFetched blanks the fetch timestamp of a stored snapshot: both
// sides fetch with the real clock and the CLI cannot inject a fixed one
// through dispatch.
var dualVolatileFetched = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

// dualVolatileArgon2 blanks the stored password hash: every run salts it
// afresh, so two correct runs never store the same bytes.
var dualVolatileArgon2 = regexp.MustCompile(`\$argon2id\$\S+`)

// dualSnapshotSeed stores the same snapshot under name so a scenario can
// rename or remove it.
func dualSnapshotSeed(t *testing.T, home, name string) {
	t.Helper()
	snap := subscription.Snapshot{
		FetchedAt: testNow,
		Nodes: []subscription.Node{
			{ID: "n1", Name: "Frankfurt 1", Type: "ss", Server: "example.invalid", Port: 8388, Country: "DE"},
			{ID: "n2", Name: "Amsterdam 1", Type: "ss", Server: "example.invalid", Port: 8389, Country: "NL"},
		},
	}
	if err := subscription.NewStore(filepath.Join(home, "subscriptions")).Save(name, snap); err != nil {
		t.Fatal(err)
	}
}

// dualSettingsSeed seeds a home whose auth pair is valid (auth.password
// requires auth.login), so every settings key can be edited independently.
func dualSettingsSeed(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Auth.Login = "alice"
	cfg.Auth.Password = config.InitPlaceholder
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	return home
}

func dualProvidersScreen(d *dualShell) *providersScreen { return d.screen.(*providersScreen) }
func dualSettingsScreen(d *dualShell) *settingsScreen   { return d.screen.(*settingsScreen) }

// dualSelectCardLine walks the card with real key presses until the line with
// the id is selected. Card movement skips disabled and read-only rows, so it
// targets a row by ID rather than treating its physical offset as a key count.
func dualSelectCardLine(t *testing.T, d *dualShell, card *tui.Card, id string) {
	t.Helper()
	switch screen := d.screen.(type) {
	case *accountsScreen:
		if screen.width >= 100 && screen.workspace.Focus() != tui.PaneCard {
			d.Keys("tab")
		} else if screen.width < 100 && !screen.workspace.Drilled {
			d.Keys("enter")
		}
	case *providersScreen:
		if screen.width >= 100 && screen.workspace.Focus() != tui.PaneCard {
			d.Keys("tab")
		} else if screen.width < 100 && !screen.workspace.Drilled {
			d.Keys("enter")
		}
	case *settingsScreen:
		if screen.width >= 100 && screen.workspace.Focus() != tui.PaneCard {
			d.Keys("tab")
		} else if screen.width < 100 && !screen.workspace.Drilled {
			d.Keys("enter")
		}
	}
	target := -1
	for i, line := range card.Lines {
		if line.ID == id {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("no card line %q", id)
	}
	if card.Lines[target].Disabled || card.Lines[target].Kind == tui.CardReadOnly {
		t.Fatalf("card line %q is not selectable", id)
	}
	d.Keys("home")
	for i := 0; i < len(card.Lines) && card.Lines[card.Selected].ID != id; i++ {
		before := card.Selected
		d.Keys("down")
		if card.Selected == before {
			break
		}
	}
	if card.Lines[card.Selected].ID != id {
		t.Fatalf("card walk stopped at %q, did not reach %q", card.Lines[card.Selected].ID, id)
	}
}

// dualChooseProviderHopOperation chooses from the real operation picker.
func dualChooseProviderHopOperation(t *testing.T, d *dualShell, value string) {
	t.Helper()
	s := dualProvidersScreen(d)
	if s.prompt == nil || s.prompt.Kind != tui.ChoicePrompt {
		t.Fatal("provider hop operation picker is not open")
	}
	choice := -1
	for i, option := range s.prompt.Options {
		if option.Value == value {
			choice = i
			break
		}
	}
	if choice < 0 {
		t.Fatalf("provider hop picker has no operation %q", value)
	}
	for i := 0; i < len(s.prompt.Options); i++ {
		selected, err := s.prompt.GetAnswer()
		if err != nil {
			t.Fatalf("read provider hop picker choice: %v", err)
		}
		if selected == value {
			break
		}
		d.Keys("down")
	}
	selected, err := s.prompt.GetAnswer()
	if err != nil || selected != value {
		t.Fatalf("provider hop picker walk did not reach operation %q", value)
	}
	d.Keys("enter")
	if value == "replace" || value == "add" {
		if s.prompt == nil || s.prompt.Kind != tui.SecretPrompt || s.prompt.Value != "" {
			t.Fatal("provider hop operation did not open a blank secret prompt")
		}
	}
}

// dualClearPrompt empties the open prompt's preset value with backspaces,
// like a user clearing the field before typing a new value.
func dualClearPrompt(d *dualShell, p *tui.Prompt) {
	d.t.Helper()
	for i := 0; i < 64 && p != nil && p.Value != ""; i++ {
		d.Keys("backspace")
	}
	if p != nil && p.Value != "" {
		d.t.Fatalf("the prompt did not clear: %q", p.Value)
	}
}

// provider.add: sub add --name/--via versus the + Add provider wizard (name,
// URL, Via, preview, review, Create).
func dualProviderAddScenario(t *testing.T) {
	const (
		url = "https://sub.invalid/alpha"
		via = "http://proxy.invalid:8080"
	)
	dualUseFakeFetch(t, testBody)
	cliHome, shellHome := dualHomes(t, func(t *testing.T) string { return providerSeed(t) })

	dualRunCLI(t, cliHome, "sub", "add", "--name", "alpha", "--via", via, url)

	d := newDualShell(t, shellHome, "Providers")
	d.Keys("end", "enter") // the + Add provider row opens the wizard
	d.Type("alpha")
	d.Keys("enter")
	d.Type(url)
	d.Keys("enter")
	d.Type(via)
	d.Keys("enter") // Via -> the preview fetch runs
	d.Keys("enter") // preview step -> review
	d.Keys("enter") // Create

	dualEqual(t, cliHome, shellHome, dualVolatileFetched)
}

// provider.refresh: sub update NAME and sub update --all versus the r and R
// keys of the Providers screen.
func dualProviderRefreshScenario(t *testing.T) {
	dualUseFakeFetch(t, testBody)
	seed := func(t *testing.T) string {
		return providerSeed(t,
			config.Subscription{Name: "p", URL: "https://p.invalid/sub"},
			config.Subscription{Name: "q", URL: "https://q.invalid/sub"},
		)
	}
	cliHome, shellHome := dualHomes(t, seed)

	dualRunCLI(t, cliHome, "sub", "update", "p")

	d := newDualShell(t, shellHome, "Providers")
	d.Keys("r")   // refresh the selected provider, then the results view
	d.Keys("esc") // leave the results view
	dualEqual(t, cliHome, shellHome, dualVolatileFetched)

	dualRunCLI(t, cliHome, "sub", "update", "--all")
	d.Keys("R") // refresh all

	dualEqual(t, cliHome, shellHome, dualVolatileFetched)
}

// provider.remove: sub rm versus the d key, the dependency preflight and the
// screen's own confirmation.
func dualProviderRemoveScenario(t *testing.T) {
	seed := func(t *testing.T) string {
		home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid/sub"})
		dualSnapshotSeed(t, home, "p")
		return home
	}
	cliHome, shellHome := dualHomes(t, seed)

	dualRunCLI(t, cliHome, "sub", "rm", "p")

	d := newDualShell(t, shellHome, "Providers")
	d.Keys("d") // preflight opens the remove confirmation
	d.Keys("y") // confirm

	dualEqual(t, cliHome, shellHome)
}

// provider.hops: sub add --pre-socks5 and sub set-socks5 (--post, --pre
// inherit, both flags with none) versus the Pre-hop/Post-hop prompts and the
// Clear pre-hop action on the provider card. The add wizard has no hop
// steps, so the shell sets every hop after the add, like a user would.
func dualProviderHopsScenario(t *testing.T) {
	const (
		url  = "https://main.invalid/sub"
		pre  = "socks5://pre.invalid:1080"
		post = "http://post.invalid:3128"
		pre2 = "socks5://pre2.invalid:1080"
	)
	dualUseFakeFetch(t, testBody)
	cliHome, shellHome := dualHomes(t, func(t *testing.T) string { return providerSeed(t) })

	dualRunCLI(t, cliHome, "sub", "add", "--name", "main", "--pre-socks5", pre, url)

	d := newDualShell(t, shellHome, "Providers")
	s := dualProvidersScreen(d)
	d.Keys("end", "enter") // + Add provider opens the wizard
	d.Type("main")
	d.Keys("enter")
	d.Type(url)
	d.Keys("enter")
	d.Keys("enter") // the optional Via left empty; the preview fetch runs
	d.Keys("enter") // preview step -> review
	d.Keys("enter") // Create
	d.Keys("enter") // drill into the provider card

	dualSelectCardLine(t, d, s.card, "pre")
	d.Keys("enter")
	dualChooseProviderHopOperation(t, d, "replace")
	d.Type(pre)
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome, dualVolatileFetched)

	dualRunCLI(t, cliHome, "sub", "set-socks5", "main", "--post", post)
	dualSelectCardLine(t, d, s.card, "post")
	d.Keys("enter")
	dualChooseProviderHopOperation(t, d, "replace")
	d.Type(post)
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome, dualVolatileFetched)

	dualRunCLI(t, cliHome, "sub", "set-socks5", "main", "--pre", "inherit")
	dualSelectCardLine(t, d, s.card, "pre")
	d.Keys("enter")
	dualChooseProviderHopOperation(t, d, "inherit")
	dualEqual(t, cliHome, shellHome, dualVolatileFetched)

	dualRunCLI(t, cliHome, "sub", "set-socks5", "main", "--pre", pre2, "--post", "none")
	dualSelectCardLine(t, d, s.card, "pre")
	d.Keys("enter")
	dualChooseProviderHopOperation(t, d, "replace")
	d.Type(pre2)
	d.Keys("enter")
	dualSelectCardLine(t, d, s.card, "post")
	d.Keys("enter")
	dualChooseProviderHopOperation(t, d, "none")
	dualEqual(t, cliHome, shellHome, dualVolatileFetched)
}

// provider.rename: sub rename versus the Rename action (the prompt is
// prefilled with the current name; ctrl+u clears it). Accounts and the
// stored snapshot must follow on both sides.
func dualProviderRenameScenario(t *testing.T) {
	seed := func(t *testing.T) string {
		home := providerSeed(t,
			config.Subscription{Name: "old", URL: "https://old.invalid/sub"},
			config.Subscription{Name: "other", URL: "https://other.invalid/sub"},
		)
		dualSnapshotSeed(t, home, "old")
		cfg, err := loadConfig(home)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Accounts = append(cfg.Accounts, config.Account{Login: "alice", Password: config.InitPlaceholder, Provider: "old"})
		if err := config.Save(config.ConfigPath(home), cfg); err != nil {
			t.Fatal(err)
		}
		return home
	}
	cliHome, shellHome := dualHomes(t, seed)

	dualRunCLI(t, cliHome, "sub", "rename", "old", "renamed")

	d := newDualShell(t, shellHome, "Providers")
	s := dualProvidersScreen(d)
	d.Keys("enter") // drill into the first provider ("old")
	dualSelectCardLine(t, d, s.card, "edit-name")
	d.Keys("enter")
	d.Keys("ctrl+u")
	d.Type("renamed")
	d.Keys("enter")

	dualEqual(t, cliHome, shellHome)
}

// provider.url: sub set --url versus the Subscription field line and the
// Edit subscription action, each opening the secret URL prompt.
func dualProviderURLScenario(t *testing.T) {
	seed := func(t *testing.T) string {
		return providerSeed(t, config.Subscription{Name: "p", URL: "https://old.invalid/sub"})
	}
	cliHome, shellHome := dualHomes(t, seed)

	dualRunCLI(t, cliHome, "sub", "set", "p", "--url", "https://new.invalid/sub")

	d := newDualShell(t, shellHome, "Providers")
	s := dualProvidersScreen(d)
	d.Keys("enter") // drill; the Subscription line is the first editable one
	dualSelectCardLine(t, d, s.card, "subscription")
	d.Keys("enter")
	d.Type("https://new.invalid/sub")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	dualRunCLI(t, cliHome, "sub", "set", "p", "--url", "https://final.invalid/sub")
	dualSelectCardLine(t, d, s.card, "edit-subscription")
	d.Keys("enter")
	d.Type("https://final.invalid/sub")
	d.Keys("enter")

	dualEqual(t, cliHome, shellHome)
}

// provider.via: sub set --via (and --via none) versus the Via field line and
// the Edit Via action; an empty answer clears the fetch proxy.
func dualProviderViaScenario(t *testing.T) {
	seed := func(t *testing.T) string {
		return providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid/sub", Via: "http://old-proxy.invalid:8080"})
	}
	cliHome, shellHome := dualHomes(t, seed)

	dualRunCLI(t, cliHome, "sub", "set", "p", "--via", "socks5://new-proxy.invalid:1080")

	d := newDualShell(t, shellHome, "Providers")
	s := dualProvidersScreen(d)
	d.Keys("enter") // drill
	dualSelectCardLine(t, d, s.card, "via")
	d.Keys("enter")
	d.Type("socks5://new-proxy.invalid:1080")
	d.Keys("enter")
	dualEqual(t, cliHome, shellHome)

	dualRunCLI(t, cliHome, "sub", "set", "p", "--via", "none")
	dualSelectCardLine(t, d, s.card, "edit-via")
	d.Keys("enter")
	d.Keys("enter") // an empty answer clears the fetch proxy

	dualEqual(t, cliHome, shellHome)
}

// settings.global: config set for every kind of settings key (plus a
// read-only config get, which must store nothing) versus the Settings tab
// prompts. The auth pair is edited password first: the pair rule
// (auth.password requires auth.login) accepts no other order from the
// default file. The stored hash differs per run and is blanked by
// dualVolatileArgon2.
func dualSettingsGlobalScenario(t *testing.T) {
	cliHome, shellHome := dualHomes(t, dualSettingsSeed)

	dualRunCLI(t, cliHome, "config", "set", "listen.port", "8080")
	dualRunCLI(t, cliHome, "config", "set", "listen.allowed_clients", "127.0.0.1")
	dualRunCLI(t, cliHome, "config", "set", "auth.password", "pw-secret-1")
	dualRunCLI(t, cliHome, "config", "set", "auth.login", "alice2")
	dualRunCLI(t, cliHome, "config", "set", "auth.allow_init_remote", "true")
	dualRunCLI(t, cliHome, "config", "set", "fetch.proxy", "socks5://fp.invalid:1080")
	dualRunCLI(t, cliHome, "config", "set", "fetch.update_interval", "30m")
	dualRunCLI(t, cliHome, "config", "set", "chain.pre_socks5", "socks5://cp.invalid:1080")
	dualRunCLI(t, cliHome, "config", "set", "chain.post_socks5", "none")
	dualRunCLI(t, cliHome, "config", "get")

	d := newDualShell(t, shellHome, "Settings")
	f := dualSettingsScreen(d)

	d.Keys("enter") // drill into Listen
	dualSelectCardLine(t, d, f.workspace.Card, "listen.port")
	d.Keys("enter")
	dualClearPrompt(d, f.prompt)
	d.Type("8080")
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "listen.allowed_clients")
	d.Keys("enter")
	dualClearPrompt(d, f.prompt)
	d.Type("127.0.0.1")
	d.Keys("enter")

	d.Keys("esc")
	d.Keys("down") // Auth
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "auth.password")
	d.Keys("enter")
	d.Type("pw-secret-1")
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "auth.login")
	d.Keys("enter")
	dualClearPrompt(d, f.prompt)
	d.Type("alice2")
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "auth.allow_init_remote")
	d.Keys("enter")
	d.Keys("down") // the choice moves from the preset false to true
	d.Keys("enter")

	d.Keys("esc")
	d.Keys("down") // Fetch
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "fetch.proxy")
	d.Keys("enter")
	d.Type("socks5://fp.invalid:1080")
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "fetch.update_interval")
	d.Keys("enter")
	d.Type("30m")
	d.Keys("enter")

	d.Keys("esc")
	d.Keys("down") // Chain
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "chain.pre_socks5")
	d.Keys("enter")
	d.Type("socks5://cp.invalid:1080")
	d.Keys("enter")
	dualSelectCardLine(t, d, f.workspace.Card, "chain.post_socks5")
	d.Keys("enter")
	d.Type("none")
	d.Keys("enter")

	dualEqual(t, cliHome, shellHome, dualVolatileArgon2)
	// The hashes are blanked above; check each really stores the password
	// that was typed.
	for name, home := range map[string]string{"CLI": cliHome, "shell": shellHome} {
		c, err := loadConfig(home)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := auth.VerifyHash(c.Auth.Password, "pw-secret-1"); err != nil || !ok {
			t.Errorf("the %s stored a hash that does not verify the typed password", name)
		}
	}
}
