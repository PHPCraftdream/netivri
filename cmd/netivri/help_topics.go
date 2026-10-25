package main

import "github.com/PHPCraftdream/netivri/internal/cli"

// The help text is ASCII only on purpose: it must read the same in every
// console code page. help_test.go checks it against the command table and
// the documented flags.

const (
	groupService = "Proxy service"
	groupSubs    = "Subscriptions and nodes"
	groupAccount = "Accounts"
	groupDiag    = "Statistics and diagnostics"
	groupOther   = "Other"
)

// helpGroups is the order of the command list in the overview.
var helpGroups = []string{groupService, groupSubs, groupAccount, groupDiag, groupOther}

const helpSummary = "local proxy service for provider subscriptions"

const helpIntro = "netivri keeps a local proxy running on one port (SOCKS5, SOCKS4 and HTTP) and " +
	"routes each login through the provider and exit country you choose. Every command prints " +
	"one document in Ktav, or in JSON with --json, so it works the same in a terminal, in scripts " +
	"and for AI agents."

var helpOverviewUsage = []string{
	"netivri {COMMAND} [{ARGUMENTS}] [flags]",
	"netivri help [{COMMAND}]",
}

var helpQuickStart = []string{
	"# 1. add a subscription (the URL is a credential, keep it private)",
	"netivri sub add https://provider.example/sub/{TOKEN} --name main",
	"# 2. create a login that exits through Germany",
	"netivri account add alice --provider main --country DE",
	"# 3. start the proxy in the background and check it",
	"netivri up",
	"netivri status",
	"# 4. use it; the first password you send as alice becomes her password",
	"curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org",
}

var helpGlobalFlags = []helpFlag{
	{"--home {DIR}", `State directory with config.ktav, subscriptions/, run/ and stats.db. ` +
		`Default: $NETIVRI_HOME, else the user config directory plus /netivri ` +
		`(%APPDATA%\netivri on Windows, ~/.config/netivri on Linux, ` +
		`~/Library/Application Support/netivri on macOS).`},
	{"--json", "Print results and errors as JSON instead of Ktav (the default). Setting NETIVRI_OUTPUT=json does the same for every run. For help it selects the machine-readable form."},
	{"--quiet", "Accepted for compatibility; changes nothing today."},
	{"--no-color", "Accepted for compatibility; there is no color output."},
	{"-h, --help", "Show help for a command, for example: netivri up --help"},
}

const helpOutput = `Every command prints one document on stdout, in Ktav by default: ` +
	`schema_version, ok and data. With --json it is one JSON object ` +
	`{"schema_version":1,"ok":true,"data":{...}}. A failure prints the same kind of document on ` +
	`stderr with a stable "code" and ends with a non-zero exit code; branch on the code, not on the ` +
	`message. Subscription URLs, passwords and hashes are never printed.`

var helpExitCodes = []helpExit{
	{cli.ExitOK, "success"},
	{cli.ExitInternal, "unexpected internal error"},
	{cli.ExitUsage, "invalid arguments or usage"},
	{cli.ExitNetwork, "network error (fetching a subscription, diagnosing a node)"},
	{cli.ExitNoDaemon, "the daemon is not running (for commands that need it)"},
	{cli.ExitNotFound, "not found (subscription, account, node, nodes for a country)"},
	{cli.ExitConflict, "conflict (port in use, daemon already running with other settings)"},
}

// helpPlanned lists what docs/cli.md describes but the binary lacks.
var helpPlanned = []string{"use", "node test", "doctor", "schema"}

func herr(code string, exit int, meaning string) helpError {
	return helpError{Code: code, Exit: exit, Meaning: meaning}
}

var helpTopics = []helpTopic{
	{
		Name:    "up",
		Group:   groupService,
		Summary: "Start the proxy (a background daemon by default)",
		Usage:   []string{"netivri up [--port {N}] [--bind {ADDR}] [--foreground] [--home {DIR}]"},
		Description: "Starts the local proxy: SOCKS5, SOCKS4/4a and HTTP (plain requests and CONNECT) on one TCP port, " +
			"told apart by the first byte of each connection. Without --foreground it starts a detached background " +
			"daemon and returns at once; running it again is safe and reports the running instance.\n\n" +
			"Settings come from config.ktav in the state directory; --port and --bind override listen.port and " +
			"listen.bind. An account with a provider or an exit country is routed only through matching nodes, " +
			"never directly. UDP is not relayed.",
		Flags: []helpFlag{
			{"--port {N}", "Local port, default 7890. 0 takes any free port; the chosen address is in the output (listen)."},
			{"--bind {ADDR}", "Address to listen on, default 127.0.0.1. Anything but loopback needs proxy authentication in config.ktav, otherwise up fails with AUTH_REQUIRED."},
			{"--foreground", "Run in this terminal until Ctrl+C or 'netivri down'. Prints one document when ready. Fails with ALREADY_RUNNING when a daemon is running."},
			{"--home {DIR}", "State directory (see the global flags in 'netivri help')."},
		},
		Examples: []helpExample{
			{"Start in the background on 127.0.0.1:7890", "netivri up"},
			{"Take any free port and read it from the output", "netivri up --port 0"},
			{"Run in the foreground, for example under a service manager", "netivri up --foreground"},
			{"Keep a separate profile with its own config, subscriptions and statistics", "netivri up --home ./work-profile"},
			{"Listen on every interface (needs auth.login and auth.password in config.ktav)", "netivri up --bind 0.0.0.0"},
			{"Check that the daemon applied your latest account and subscription changes (see reload)", "netivri status"},
		},
		Errors: []helpError{
			herr("ALREADY_RUNNING", cli.ExitConflict, "a daemon is running and --port or --bind differ, or --foreground was given"),
			herr("PORT_IN_USE", cli.ExitConflict, "the listen port is taken"),
			herr("AUTH_REQUIRED", cli.ExitUsage, "non-loopback --bind without proxy authentication"),
			herr("NO_NODES_FOR_COUNTRY", cli.ExitNotFound, "an account's provider or country has no stored nodes; run 'netivri sub update'"),
			herr("DAEMON_START_FAILED", cli.ExitInternal, "the daemon failed to start; see run/netivri.log in the state directory"),
		},
		SeeAlso: []string{"down", "status", "account add"},
	},
	{
		Name:    "down",
		Group:   groupService,
		Summary: "Stop the daemon",
		Usage:   []string{"netivri down [--home {DIR}]"},
		Description: "Stops the daemon through its control channel and waits until it is gone, up to 15 seconds. " +
			"It is safe to repeat: with nothing running it succeeds and reports was_running false. It never kills a " +
			"process by pid.",
		Examples: []helpExample{
			{"Stop the daemon", "netivri down"},
			{"Stop the daemon of another profile", "netivri down --home ./work-profile"},
		},
		Errors:  []helpError{herr("DAEMON_STOP_TIMEOUT", cli.ExitInternal, "the daemon did not stop within 15 seconds")},
		SeeAlso: []string{"up", "status"},
	},
	{
		Name:    "status",
		Group:   groupService,
		Summary: "Show the running daemon",
		Usage:   []string{"netivri status [--home {DIR}]"},
		Description: "Reports the live state of the daemon: listen address and protocols, authentication mode, accounts " +
			"with their provider and exit country, uptime and version and, while accounts have routes, the warm-up " +
			"probes, the statistics store and the sticky-routing counters.\n\n" +
			"The 'activity' list shows who is using the proxy now: for every account whether it is active (has open " +
			"connections), how many connections and bytes, and through which nodes, providers and countries, the " +
			"busiest first. 'active_accounts' counts the active ones.\n\n" +
			"It exits with 4 when no daemon is running, so it works as a check in scripts.",
		Examples: []helpExample{
			{"Show the daemon and who is using it (see activity)", "netivri status"},
			{"Start the daemon only when it is not running (POSIX shell)", "netivri status >/dev/null 2>&1 || netivri up"},
		},
		Errors:  []helpError{herr("DAEMON_NOT_RUNNING", cli.ExitNoDaemon, "nothing is running; start it with 'netivri up'")},
		SeeAlso: []string{"up", "stats"},
	},

	{
		Name:    "sub",
		Summary: "Manage provider subscriptions",
		Usage:   []string{"netivri sub {add|list|update|rm|countries} [arguments]"},
		Description: "A subscription is the link a provider gives you; it lists proxy nodes as Clash/Mihomo YAML or as " +
			"a base64 list of share links (vless://, vmess://, trojan://, ss://, hysteria2://). The URL is a credential: " +
			"netivri never prints or logs it. Every fetch goes through a strict proxy path and never silently directly.",
		Examples: []helpExample{
			{"Add a subscription, look at it and refresh it later", "netivri sub add https://provider.example/sub/{TOKEN} --name main\nnetivri sub list\nnetivri sub update --all"},
		},
		SeeAlso: []string{"account", "node list"},
	},
	{
		Name:    "sub add",
		Group:   groupSubs,
		Summary: "Add a subscription",
		Usage:   []string{"netivri sub add {URL} [--name {NAME}] [--via {PROXY}]"},
		Description: "Fetches the subscription, parses it, stores its nodes and only then adds it to the config. If " +
			"anything fails nothing is added. The fetch goes through --via when given, otherwise through fetch.proxy " +
			"from config.ktav, otherwise directly; environment proxies are never used.\n\n" +
			"It is idempotent by URL: adding a known URL again re-fetches it under its existing name and reports " +
			"existing true. The URL is never printed.",
		Flags: []helpFlag{
			{"--name {NAME}", "Name of the subscription (letters, digits, . _ -; at most 64). Default: derived from the host of the URL. A name used by a different URL is SUB_EXISTS_NAME."},
			{"--via {PROXY}", "Fetch through this proxy: an http(s):// or socks5(h):// URL."},
		},
		Examples: []helpExample{
			{"Add a subscription; the name comes from the host", "netivri sub add https://provider.example/sub/{TOKEN}"},
			{"Give it a name you will use in accounts", "netivri sub add https://provider.example/sub/{TOKEN} --name main"},
			{"The provider is blocked directly: fetch through a local proxy", "netivri sub add https://provider.example/sub/{TOKEN} --name main --via socks5h://127.0.0.1:1080"},
		},
		Errors: []helpError{
			herr("FETCH_URL_INVALID", cli.ExitUsage, "the subscription or proxy URL is not valid"),
			herr("FETCH_PROXY_FAILED", cli.ExitNetwork, "the proxy could not be reached"),
			herr("FETCH_FAILED", cli.ExitNetwork, "the request to the provider failed"),
			herr("FETCH_HTTP_STATUS", cli.ExitNetwork, "the provider answered with a non-2xx status"),
			herr("SUBSCRIPTION_EMPTY", cli.ExitNetwork, "no valid nodes in the body"),
			herr("SUB_EXISTS_NAME", cli.ExitConflict, "the name belongs to a different URL"),
		},
		SeeAlso: []string{"sub countries", "sub update", "account add"},
	},
	{
		Name:    "sub list",
		Group:   groupSubs,
		Summary: "List subscriptions",
		Usage:   []string{"netivri sub list [--home {DIR}]"},
		Description: "Shows each subscription: its name, the host of its URL, whether a --via proxy is set, how many " +
			"nodes are stored, when they were fetched (null until the first fetch) and the quota and expiry the " +
			"provider reports (null without a subscription-userinfo header). The URL and the proxy are never " +
			"printed.",
		Examples: []helpExample{{"List the subscriptions", "netivri sub list"}},
		SeeAlso:  []string{"sub update", "sub rm"},
	},
	{
		Name:    "sub update",
		Group:   groupSubs,
		Summary: "Re-fetch subscriptions",
		Usage:   []string{"netivri sub update [{SUBSCRIPTION} | --all]"},
		Description: "Fetches subscriptions again and replaces their stored nodes. With a {SUBSCRIPTION} only that subscription, " +
			"with --all or no argument every one, in config order. Each result also reports the quota and expiry the " +
			"provider sent in its subscription-userinfo header (null without one). A subscription that fails keeps its " +
			"old nodes. When any fails, the results are still printed and the command ends with SUB_UPDATE_FAILED.\n\n" +
			"A running daemon applies the new nodes to its accounts within a few seconds, and re-fetches every " +
			"subscription by itself once an hour (fetch.update_interval in config.ktav), so this command is for " +
			"refreshing on demand.",
		Flags: []helpFlag{{"--all", "Update every subscription (the default without a {SUBSCRIPTION})."}},
		Examples: []helpExample{
			{"Refresh every subscription", "netivri sub update"},
			{"Refresh one subscription", "netivri sub update main"},
		},
		Errors: []helpError{
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "no subscription with that name"),
			herr("SUB_UPDATE_FAILED", cli.ExitNetwork, "at least one subscription failed; the results are printed on stdout"),
		},
		SeeAlso: []string{"sub list", "up"},
	},
	{
		Name:    "sub rm",
		Group:   groupSubs,
		Summary: "Remove a subscription",
		Usage:   []string{"netivri sub rm {SUBSCRIPTION}"},
		Description: "Removes the subscription from the config and deletes its stored nodes. It is not idempotent. " +
			"It refuses while an account uses the subscription as its provider; free the account first with " +
			"'netivri account set-provider {ACCOUNT} --none'.",
		Examples: []helpExample{{"Remove a subscription", "netivri sub rm backup"}},
		Errors: []helpError{
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "no subscription with that name"),
			herr("SUB_IN_USE", cli.ExitConflict, "an account uses it as its provider; nothing was removed"),
		},
		SeeAlso: []string{"account set-provider"},
	},
	{
		Name:    "sub countries",
		Group:   groupSubs,
		Summary: "Show the exit countries of a provider",
		Usage: []string{
			"netivri sub countries [{SUBSCRIPTION}]",
			"netivri sub countries --url {URL} [--via {PROXY}]",
		},
		Description: "Shows which exit countries a provider offers, so you know what to give --country. With a {SUBSCRIPTION} " +
			"it reads that provider's stored nodes without any network. With no argument it lists every provider and " +
			"the union of their countries. With --url it fetches a subscription through the same strict path as " +
			"'sub add' and prints its countries without storing anything.\n\n" +
			"A node's country is read from its name; nodes where that is not possible are counted as unknown.",
		Flags: []helpFlag{
			{"--url {URL}", "Count the countries of this subscription URL without storing it. Cannot be combined with a {SUBSCRIPTION}."},
			{"--via {PROXY}", "Fetch --url through this proxy. Needs --url."},
		},
		Examples: []helpExample{
			{"Every provider and all countries", "netivri sub countries"},
			{"One provider", "netivri sub countries main"},
			{"Look before you add", "netivri sub countries --url https://provider.example/sub/{TOKEN}"},
		},
		Errors: []helpError{
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "no subscription with that name"),
			herr("SUB_SNAPSHOT_INVALID", cli.ExitInternal, "the stored nodes are corrupt; run 'netivri sub update'"),
		},
		SeeAlso: []string{"account add", "sub add"},
	},
	{
		Name:        "node",
		Summary:     "Inspect the stored nodes",
		Usage:       []string{"netivri node list [--sub {SUBSCRIPTION}] [--country {COUNTRY}]"},
		Description: "Nodes are the proxy servers listed by your subscriptions.",
		Examples:    []helpExample{{"List every stored node", "netivri node list"}},
		SeeAlso:     []string{"sub add"},
	},
	{
		Name:    "node list",
		Group:   groupSubs,
		Summary: "List the stored nodes",
		Usage:   []string{"netivri node list [--sub {SUBSCRIPTION}] [--country {COUNTRY}]"},
		Description: "Prints the stored nodes of every subscription, or of one, in subscription order, with their id, " +
			"name, type, server, port and country. The country is read from the node name, so treat it as a hint " +
			"about the exit, not as proof. Credentials are never printed.",
		Flags: []helpFlag{
			{"--sub {SUBSCRIPTION}", "Only the nodes of this subscription."},
			{"--country {COUNTRY}", "Only nodes of this country: an ISO 3166-1 alpha-2 code, any case."},
		},
		Examples: []helpExample{
			{"Every stored node", "netivri node list"},
			{"Nodes of Germany", "netivri node list --country DE"},
			{"Nodes of the Netherlands in one subscription", "netivri node list --sub main --country nl"},
		},
		Errors:  []helpError{herr("SUB_NOT_FOUND", cli.ExitNotFound, "no subscription with that name")},
		SeeAlso: []string{"sub countries", "stats"},
	},

	{
		Name:    "account",
		Summary: "Manage proxy logins with a provider and an exit country",
		Usage:   []string{"netivri account {add|clone|list|show|set-country|set-provider|rm} [arguments]"},
		Description: "An account is a proxy login. Several accounts share one port. Each may name a provider (a " +
			"subscription) and an exit country, and its traffic leaves only through matching nodes, with no fallback " +
			"to a direct connection or to a country or provider it does not name. An account with neither uses the " +
			"default route, which is direct.\n\n" +
			"Both may be a comma-separated list in priority order: the countries come first, and inside a country " +
			"the providers, so 'main,backup' with 'DE,NL' tries main in DE, backup in DE, main in NL, backup in NL. " +
			"New connections use the first of these that has a node that is up; the later ones are fallbacks. In a " +
			"country list * stands for every country not named in it, so 'DE,*' is Germany first and then anywhere; " +
			"quote it in shells that expand globs.\n\n" +
			"A new account has the password placeholder 'init': the first client that connects with the login sets the " +
			"password, which is stored as an argon2id hash.\n\n" +
			"A running daemon applies account changes by itself within a few seconds. Its listener restarts on the " +
			"same port, so open connections end; 'netivri status' shows the outcome under 'reload'.",
		Examples: []helpExample{
			{"Create a login and list the logins", "netivri account add alice --provider main --country DE\nnetivri account list"},
			{"Show one account and its route", "netivri account show alice"},
		},
		SeeAlso: []string{"up", "sub countries"},
	},
	{
		Name:    "account add",
		Group:   groupAccount,
		Summary: "Create an account",
		Usage:   []string{"netivri account add {ACCOUNT} [--provider {SUBSCRIPTION}[,{SUBSCRIPTION}...]] [--country {COUNTRY}[,{COUNTRY}...]]"},
		Description: "Creates a proxy login with the password placeholder 'init': the first client that connects with " +
			"this login sets the password. A login uses the characters A-Z a-z 0-9 . _ @ - (and is not 'init').\n\n" +
			"Repeating the command with the same providers and countries, in the same order, succeeds and reports " +
			"existing true; any difference for an existing login is ACCOUNT_EXISTS (use set-country or " +
			"set-provider to change it). " +
			"A running daemon applies the new account within a few seconds.",
		Flags: []helpFlag{
			{"--provider {SUBSCRIPTION}[,{SUBSCRIPTION}...]", "Route only through the nodes of this subscription, or of several, separated by commas and in priority order. Each must be a configured subscription."},
			{"--country {COUNTRY}[,{COUNTRY}...]", "Route only through nodes of this country: an ISO 3166-1 alpha-2 code such as DE, NL or FI, any case; or several codes, separated by commas and in priority order. * stands for every country not named in the list: DE,* is Germany first, then anywhere; * alone is any country. Quote it where the shell expands globs."},
		},
		Examples: []helpExample{
			{"Exit through Germany, using the subscription main", "netivri account add alice --provider main --country DE"},
			{"Exit through the Netherlands, any subscription that has such nodes", "netivri account add bob --country NL"},
			{"Any country of one subscription", "netivri account add carol --provider main"},
			{"Germany first, then the Netherlands; main first, then backup", "netivri account add dave --provider main,backup --country DE,NL"},
			{"Connect: the first password you send for alice becomes her password", "curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org"},
		},
		Errors: []helpError{
			herr("ACCOUNT_INVALID", cli.ExitUsage, "the login is not valid"),
			herr("COUNTRY_INVALID", cli.ExitUsage, "the country is not an ISO 3166-1 alpha-2 code"),
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "the provider is not a configured subscription"),
			herr("ACCOUNT_EXISTS", cli.ExitConflict, "the login exists with a different provider or country"),
		},
		SeeAlso: []string{"account list", "account clone", "sub countries", "up"},
	},
	{
		Name:    "account clone",
		Group:   groupAccount,
		Summary: "Create an account from an existing one",
		Usage: []string{
			"netivri account clone {SOURCE} {ACCOUNT} [--password {PASSWORD}|init] [--provider {SUBSCRIPTION}[,{SUBSCRIPTION}...]] [--country {COUNTRY}[,{COUNTRY}...]]",
		},
		Description: "Creates {ACCOUNT} with the provider and exit country of {SOURCE} and, unless --password says " +
			"otherwise, with the same password: the stored hash is copied, so both logins accept the same password. " +
			"If {SOURCE} still has the placeholder 'init', so does the copy.\n\n" +
			"--password {PASSWORD} gives the new account a password of its own (1 to 1024 bytes, stored as an " +
			"argon2id hash); --password init leaves it to the first client that connects. --provider and --country " +
			"replace the copied route, each as in 'account add', and the new route must have stored nodes, otherwise " +
			"nothing is created. {SOURCE} is never changed. A running daemon picks the new account up within a few " +
			"seconds.",
		Flags: []helpFlag{
			{"--password {PASSWORD}", "A new password for {ACCOUNT}. Default: copy the password of {SOURCE}."},
			{"--password init", "Use the placeholder: the first password a client sends for {ACCOUNT} becomes its password."},
			{"--provider {SUBSCRIPTION}[,...]", "Replace the copied provider (or list of providers, in priority order)."},
			{"--country {COUNTRY}[,...]", "Replace the copied exit country (or list, in priority order; * stands for every country not named)."},
		},
		Examples: []helpExample{
			{"A second login with the same route and the same password", "netivri account clone alice bob"},
			{"The same route, a password of its own", "netivri account clone alice bob --password choose-a-password"},
			{"The same route, the first client sets the password", "netivri account clone alice bob --password init"},
			{"The same login settings, another country", "netivri account clone alice carol --password init --country NL"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "{SOURCE} does not exist"),
			herr("ACCOUNT_EXISTS", cli.ExitConflict, "{ACCOUNT} exists already (or is the login of the proxy authentication)"),
			herr("ACCOUNT_INVALID", cli.ExitUsage, "a login is not valid"),
			herr("COUNTRY_INVALID", cli.ExitUsage, "the country is not an ISO 3166-1 alpha-2 code"),
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "the provider is not a configured subscription"),
			herr("NO_NODES_FOR_COUNTRY", cli.ExitNotFound, "the replaced route has no stored nodes"),
		},
		Notes: []string{
			"A password given on the command line is visible in the shell history and to other local users in the " +
				"process list. Use --password init to avoid typing it.",
			"The password is never printed; 'password' in the result says copied, new or init.",
		},
		SeeAlso: []string{"account add", "account list", "account set-country"},
	},
	{
		Name:    "account list",
		Group:   groupAccount,
		Summary: "List accounts",
		Usage:   []string{"netivri account list [--home {DIR}]"},
		Description: "Shows each account: login, provider, exit_country and password_set, which is false until the " +
			"first client claims the 'init' placeholder. Passwords and hashes are never shown.",
		Examples: []helpExample{{"List the accounts", "netivri account list"}},
		SeeAlso:  []string{"account add", "status"},
	},
	{
		Name:    "account show",
		Group:   groupAccount,
		Summary: "Show an account and its route tiers",
		Usage:   []string{"netivri account show {ACCOUNT} [--home {DIR}]"},
		Description: "Shows one account: its login, its configured provider and exit_country lists, and its route " +
			"as ordered tiers. The countries come first in their list order, and inside a country the providers " +
			"in their list order, so a tier is the nodes of one (country, provider) pair. A * in the country list " +
			"is the nodes of every country the list does not name, at its place in the order. Without a provider " +
			"list every provider shares one tier per country.\n\n" +
			"Per node the command shows its label ({PROVIDER}/{NODE_ID}), provider, country and tier index and, " +
			"when the daemon runs, live data: alive or down with the last probe delay, and the account's live " +
			"connections through the node. A (country, provider) pair without nodes is listed as a skipped tier " +
			"with a reason and does not consume a tier index, so the indexes match the order the daemon actually " +
			"tries.\n\n" +
			"Without a running daemon the static part is still shown and live is false (the live fields are null). " +
			"Passwords, hashes and subscription URLs are never shown.",
		Examples: []helpExample{
			{"Show alice and her route tiers", "netivri account show alice"},
			{"While the daemon runs: alive, delay and live connections per node", "netivri account show alice"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "no account with that login"),
			herr("ACCOUNT_INVALID", cli.ExitUsage, "the login is not valid"),
		},
		SeeAlso: []string{"account list", "status", "stats"},
	},
	{
		Name:    "account set-country",
		Group:   groupAccount,
		Summary: "Change or clear the exit country of an account",
		Usage: []string{
			"netivri account set-country {ACCOUNT}[,{ACCOUNT}...] {COUNTRY}[,{COUNTRY}...]",
			"netivri account set-country --all {COUNTRY}[,{COUNTRY}...]",
			"netivri account set-country {ACCOUNT}[,{ACCOUNT}...] --none",
		},
		Description: "Sets the exit country of existing accounts, or clears it with --none. The value is one code or " +
			"several, separated by commas and in priority order (DE,NL). {ACCOUNT} is the login of an " +
			"account, for example alice; several logins are separated by commas, with no spaces, and --all means every " +
			"account.\n\n" +
			"Before anything is written, every account is checked against the stored nodes of its own subscription (of " +
			"all subscriptions when it has none): when it would be left without a node in the new country, nothing is " +
			"changed for any account and the command ends with NO_NODES_FOR_COUNTRY, naming the account. See what a " +
			"subscription offers with 'netivri sub countries'. An account whose value does not change is not checked. " +
			"A running daemon applies the change within a few seconds.\n\n" +
			"With one login the result is {\"account\": ...}; with several logins or --all it is {\"accounts\": [...]}.",
		Flags: []helpFlag{
			{"--all", "Change every account instead of naming them."},
			{"--none", "Clear the country instead of setting one."},
		},
		Examples: []helpExample{
			{"Move alice to the Netherlands", "netivri account set-country alice NL"},
			{"Move alice and bob together", "netivri account set-country alice,bob DE"},
			{"Germany first, the Netherlands as the fallback", "netivri account set-country alice DE,NL"},
			{"Germany first, then any country that has a node", "netivri account set-country alice 'DE,*'"},
			{"Any country at all", "netivri account set-country alice '*'"},
			{"Move every account to Finland", "netivri account set-country --all FI"},
			{"Let alice use every country of her provider", "netivri account set-country alice --none"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "an account in the list does not exist, or --all with no accounts"),
			herr("ACCOUNT_INVALID", cli.ExitUsage, "a login in the list is not valid"),
			herr("COUNTRY_INVALID", cli.ExitUsage, "the country is not an ISO 3166-1 alpha-2 code"),
			herr("NO_NODES_FOR_COUNTRY", cli.ExitNotFound, "an account would have no stored node in that country; nothing was changed"),
		},
		SeeAlso: []string{"account set-provider", "sub countries"},
	},
	{
		Name:    "account set-provider",
		Group:   groupAccount,
		Summary: "Change or clear the provider of an account",
		Usage: []string{
			"netivri account set-provider {ACCOUNT}[,{ACCOUNT}...] {SUBSCRIPTION}[,{SUBSCRIPTION}...]",
			"netivri account set-provider --all {SUBSCRIPTION}[,{SUBSCRIPTION}...]",
			"netivri account set-provider {ACCOUNT}[,{ACCOUNT}...] --none",
		},
		Description: "Binds existing accounts to a configured subscription, or to several separated by commas and in " +
			"priority order (main,backup), or clears the binding with --none. " +
			"{ACCOUNT} is the login of an account, for example alice; {SUBSCRIPTION} is the name of a subscription, for " +
			"example main (see 'netivri sub list'). Several logins are separated by commas, with no spaces, and --all " +
			"means every account.\n\n" +
			"Before anything is written, every account is checked against the stored nodes of the new subscription and " +
			"its own exit country: when it would be left without a node, nothing is changed for any account and the " +
			"command ends with NO_NODES_FOR_COUNTRY, naming the account. Refresh the lists with 'netivri sub update'. " +
			"An account whose value does not change is not checked. A running daemon applies the change within a few " +
			"seconds.\n\n" +
			"With one login the result is {\"account\": ...}; with several logins or --all it is {\"accounts\": [...]}.",
		Flags: []helpFlag{
			{"--all", "Change every account instead of naming them."},
			{"--none", "Clear the provider instead of setting one."},
		},
		Examples: []helpExample{
			{"Route alice through the subscription main", "netivri account set-provider alice main"},
			{"Switch alice and bob to the subscription own", "netivri account set-provider alice,bob own"},
			{"main first, own as the fallback", "netivri account set-provider alice main,own"},
			{"Switch every account to the subscription own", "netivri account set-provider --all own"},
			{"Free alice from her provider, for example before 'sub rm'", "netivri account set-provider alice --none"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "an account in the list does not exist, or --all with no accounts"),
			herr("ACCOUNT_INVALID", cli.ExitUsage, "a login in the list is not valid"),
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "the subscription is not configured"),
			herr("NO_NODES_FOR_COUNTRY", cli.ExitNotFound, "an account would have no stored node on that subscription; nothing was changed"),
		},
		SeeAlso: []string{"account set-country", "sub rm"},
	},
	{
		Name:    "account rm",
		Group:   groupAccount,
		Summary: "Remove an account",
		Usage:   []string{"netivri account rm {ACCOUNT}"},
		Description: "Removes the account. It is not idempotent. A running daemon applies the removal within a few " +
			"seconds and ends the account's open connections.",
		Examples: []helpExample{{"Remove alice", "netivri account rm alice"}},
		Errors:   []helpError{herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "no account with that login")},
		SeeAlso:  []string{"account list"},
	},

	{
		Name:    "stats",
		Group:   groupDiag,
		Summary: "Show recorded node health, delay and throughput, per node or per account",
		Usage: []string{
			"netivri stats [--sub {SUBSCRIPTION}] [--country {COUNTRY}] [--node {NODE}] [--account {ACCOUNT}] [--since {DURATION}] [--by {node|country|provider|account}]",
		},
		Description: "Shows what the daemon recorded about the exit nodes: the health and delay from the background " +
			"probes, the dial time and outcome of real connections, and the throughput of real traffic. It reads " +
			"{DIR}/stats.db read-only, so it works while the daemon runs and after it stopped. Statistics exist " +
			"only for accounts that have a provider or an exit country.\n\n" +
			"The daemon also records per account which nodes it used, the dial outcomes and the traffic " +
			"(bytes up and down). With --by account the result has one row per login; with --account the " +
			"node, country and provider rows come from that login's own samples (dials and bytes only).",
		Flags: []helpFlag{
			{"--sub {SUBSCRIPTION}", "Only the nodes of this subscription (provider)."},
			{"--country {COUNTRY}", "Only the nodes of this country."},
			{"--node {NODE}", "Only this node. A label is {PROVIDER}/{NODE_ID}, as in 'netivri node list' (sub and id)."},
			{"--since {DURATION}", "Window: 90m, 24h, 7d and so on. Default 24h, at most 365d."},
			{"--by {GROUP}", "node (default), country, provider or account. Grouped output sums counters and leaves percentiles out."},
			{"--account {ACCOUNT}", "Only this account. Works with every --by mode; with node, country and provider the rows come from the account's own samples: dials and bytes only."},
		},
		Examples: []helpExample{
			{"Every node over the last day", "netivri stats"},
			{"Compare countries", "netivri stats --by country"},
			{"Compare providers over a week", "netivri stats --by provider --since 7d"},
			{"German nodes of one subscription over the last 90 minutes", "netivri stats --sub main --country DE --since 90m"},
			{"One node", "netivri stats --node main/a1b2c3"},
			{"Traffic per account", "netivri stats --by account"},
			{"One account", "netivri stats --by account --account alice"},
		},
		Errors: []helpError{
			herr("STATS_UNAVAILABLE", cli.ExitNotFound, "no statistics database yet: nothing was recorded"),
			herr("USAGE", cli.ExitUsage, "invalid --since, --by or --country"),
		},
		SeeAlso: []string{"diag dial", "status"},
	},
	{
		Name:    "logs",
		Group:   groupDiag,
		Summary: "Show the daemon log",
		Usage:   []string{"netivri logs [--follow] [--level {debug|info|warn|error}] [--since {DURATION}] [--tail {N}] [--home {DIR}]"},
		Description: "Shows what the daemon wrote to {DIR}/run/netivri.log: start and stop, applied and rejected " +
			"configuration changes, subscription refreshes, storage problems and route nodes going down or coming back, " +
			"each with a time and a level. It " +
			"reads the file, so it works while the daemon runs and after it stopped or failed to start. Passwords, " +
			"tokens and subscription URLs are never logged.\n\n" +
			"Without --follow the result is one document with the newest entries. With --follow the command keeps " +
			"running and prints new entries as lines until Ctrl+C; with --json each line is one JSON object (NDJSON).",
		Flags: []helpFlag{
			{"--follow", "Keep running and print new entries. Waits for the log when the daemon has not started yet."},
			{"--level {LEVEL}", "Only this level and above: debug, info, warn or error. Default: all."},
			{"--since {DURATION}", "Only the last period: 90m, 24h, 7d and so on, at most 365d. Default: no limit."},
			{"--tail {N}", "At most the newest N entries (default 100, 0 for all). With --follow it limits the backlog shown first."},
		},
		Examples: []helpExample{
			{"The newest entries", "netivri logs"},
			{"Problems of the last day", "netivri logs --level warn --since 24h"},
			{"Watch the daemon live", "netivri logs --follow"},
			{"Stream to a script, one JSON object per line", "netivri logs --follow --json"},
		},
		Errors: []helpError{
			herr("LOG_NOT_FOUND", cli.ExitNotFound, "the daemon has not written a log yet"),
			herr("USAGE", cli.ExitUsage, "invalid --level, --since or --tail"),
		},
		Notes:   []string{"The log is moved to netivri.log.1 when it grows past 8 MiB at the next start; 'logs' reads the current file only."},
		SeeAlso: []string{"status", "stats"},
	},
	{
		Name:        "diag",
		Summary:     "Measure the real connection path",
		Usage:       []string{"netivri diag dial --account {ACCOUNT} | --node {NODE} [flags]"},
		Description: "Diagnostics that measure instead of guess. Only 'dial' exists today.",
		Examples:    []helpExample{{"Where does a new connection spend its time?", "netivri diag dial --account alice"}},
		SeeAlso:     []string{"stats"},
	},
	{
		Name:    "diag dial",
		Group:   groupDiag,
		Summary: "Measure where a new connection spends its time",
		Usage:   []string{"netivri diag dial --account {ACCOUNT} | --node {NODE} [--samples {N}] [--idle {LIST|off}] [--max-nodes {N}] [--url {URL}]"},
		Description: "Takes a few samples of a new connection through a node and reports, per phase, how long it took " +
			"(dns, tcp, tls for nodes that expose it, and first_byte; p50, p90 and max) and how many attempts failed. " +
			"Then it tests whether a prepared connection survives sitting idle for 0.5 s, 1 s, 2 s and so on, and " +
			"suggests a pool TTL. Use it to see whether DNS, the node or the handshake is the slow part.\n\n" +
			"Give exactly one of --account and --node. It runs in its own process, without the daemon, and sends a " +
			"few small HTTP requests through each node to the probe URL. The output contains node labels only, " +
			"never addresses.",
		Flags: []helpFlag{
			{"--account {ACCOUNT}", "Diagnose the route nodes of this account, in route order (at most --max-nodes)."},
			{"--node {NODE}", "Diagnose this one node: {PROVIDER}/{NODE_ID}, as in 'netivri stats'."},
			{"--samples {N}", "Samples per node, 1 to 20 (default 5)."},
			{"--idle {LIST}", "Idle times for the survival test: up to 10 durations such as 500ms,2s, ascending; or 'off' to skip the test. Default: a built-in ladder."},
			{"--max-nodes {N}", "At most this many route nodes of an account, 1 to 50 (default 6)."},
			{"--url {URL}", "Probe URL, http only. Default: the warm-up URL (www.gstatic.com)."},
		},
		Examples: []helpExample{
			{"The nodes that account alice would use", "netivri diag dial --account alice"},
			{"One node with more samples", "netivri diag dial --node main/a1b2c3 --samples 10"},
			{"Skip the idle test to finish sooner", "netivri diag dial --account alice --idle off"},
			{"Test idle survival at your own times", "netivri diag dial --account alice --idle 500ms,2s,8s"},
			{"Probe another http URL", "netivri diag dial --node main/a1b2c3 --url http://example.org/"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "no account with that login"),
			herr("NO_ROUTE", cli.ExitNotFound, "the account has neither a provider nor an exit country"),
			herr("NODE_NOT_FOUND", cli.ExitNotFound, "no node with that label"),
			herr("DIAG_FAILED", cli.ExitNetwork, "the run could not proceed, for example an unusable stored node"),
		},
		Notes:   []string{"Failed requests are not errors: they are counted in the failures of each node."},
		SeeAlso: []string{"stats", "account add"},
	},

	{
		Name:        "version",
		Group:       groupOther,
		Summary:     "Print the version",
		Usage:       []string{"netivri version"},
		Description: "Prints the version of this binary. A build that is not a release reports a dev version.",
		Examples:    []helpExample{{"Show the version", "netivri version"}},
	},
	{
		Name:    "help",
		Group:   groupOther,
		Summary: "Show this help",
		Usage: []string{
			"netivri help [{COMMAND} [{SUBCOMMAND}]]",
			"netivri {COMMAND} [{SUBCOMMAND}] --help",
		},
		Description: "Without a command prints the overview. With one prints its usage, flags, examples and the " +
			"errors it can return. Running 'netivri' with no arguments does the same as 'netivri help'.\n\n" +
			"Help is text for people. Add --json to get one JSON object instead, for scripts and agents.",
		Flags: []helpFlag{{"--json", "Print the help as one JSON object."}},
		Examples: []helpExample{
			{"The overview", "netivri help"},
			{"One command", "netivri help up"},
			{"A subcommand, either way", "netivri help sub add\nnetivri sub add --help"},
			{"For a script or an agent", "netivri help account add --json"},
		},
	},
}
