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
	// groupConcepts holds the concept topics: not commands, but things an
	// operator with only the binary needs to learn from help alone.
	groupConcepts = "Concepts"
	groupOther    = "Other"
)

// helpGroups is the order of the command list in the overview.
var helpGroups = []string{groupService, groupSubs, groupAccount, groupDiag, groupConcepts, groupOther}

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
	{cli.ExitNoAngel, "the angel is not running (for commands that need it)"},
	{cli.ExitNotFound, "not found (subscription, account, node, nodes for a country)"},
	{cli.ExitConflict, "conflict (port in use, angel already running with other settings)"},
	{cli.ExitCheckFailed, "doctor: at least one check failed (the document on stdout says which)"},
}

// helpPlanned lists what docs/cli.md describes but the binary lacks.
var helpPlanned = []string{"use", "node test", "schema"}

func herr(code string, exit int, meaning string) helpError {
	return helpError{Code: code, Exit: exit, Meaning: meaning}
}

var helpTopics = []helpTopic{
	{
		Name:    "up",
		Group:   groupService,
		Summary: "Start the proxy (a background angel by default)",
		Usage:   []string{"netivri up [--port {N}] [--bind {ADDR}] [--foreground] [--home {DIR}]"},
		Description: "Starts the local proxy: SOCKS5, SOCKS4/4a and HTTP (plain requests and CONNECT) on one TCP port, " +
			"told apart by the first byte of each connection. Without --foreground it starts a detached background " +
			"angel and returns at once; running it again is safe and reports the running instance.\n\n" +
			"Settings come from config.ktav in the state directory; --port and --bind override listen.port and " +
			"listen.bind. An account with a provider or an exit country is routed only through matching nodes, " +
			"never directly. UDP is not relayed.\n\n" +
			"listen.allowed_clients in config.ktav restricts which client addresses may connect at all " +
			"(for example loopback or 10.0.0.0/8); it is checked before any handshake, is not a replacement " +
			"for authentication, and each account's own allowed_clients can only narrow it.",
		Flags: []helpFlag{
			{"--port {N}", "Local port, default 7890. 0 takes any free port; the chosen address is in the output (listen)."},
			{"--bind {ADDR}", "Address to listen on, default 127.0.0.1. Anything but loopback needs proxy authentication (an account, or auth.login and auth.password in config.ktav; an init password also needs auth.allow_init_remote: true), otherwise up fails with AUTH_REQUIRED."},
			{"--foreground", "Run in this terminal until Ctrl+C or 'netivri down'. Prints one document when ready. Fails with ALREADY_RUNNING when an angel is running."},
			{"--home {DIR}", "State directory (see the global flags in 'netivri help')."},
		},
		Examples: []helpExample{
			{"Start in the background on 127.0.0.1:7890", "netivri up"},
			{"Take any free port and read it from the output", "netivri up --port 0"},
			{"Run in the foreground, for example under a service manager", "netivri up --foreground"},
			{"Keep a separate profile with its own config, subscriptions and statistics", "netivri up --home ./work-profile"},
			{"Listen on every interface (needs an account or auth.login and auth.password in config.ktav)", "netivri up --bind 0.0.0.0"},
			{"Check that the angel applied your latest account and subscription changes (see reload)", "netivri status"},
		},
		Errors: []helpError{
			herr("ALREADY_RUNNING", cli.ExitConflict, "an angel is running and --port or --bind differ, or --foreground was given"),
			herr("PORT_IN_USE", cli.ExitConflict, "the listen port is taken"),
			herr("AUTH_REQUIRED", cli.ExitUsage, "non-loopback --bind without proxy authentication"),
			herr("NO_NODES_FOR_COUNTRY", cli.ExitNotFound, "an account's provider or country has no stored nodes; run 'netivri sub update'"),
			herr("ANGEL_START_FAILED", cli.ExitInternal, "the angel failed to start; see run/netivri.log in the state directory"),
		},
		SeeAlso: []string{"down", "status", "account add"},
	},
	{
		Name:    "down",
		Group:   groupService,
		Summary: "Stop the angel",
		Usage:   []string{"netivri down [--home {DIR}]"},
		Description: "Stops the angel through its control channel and waits until it is gone, up to 15 seconds. " +
			"It is safe to repeat: with nothing running it succeeds and reports was_running false. It never kills a " +
			"process by pid.",
		Examples: []helpExample{
			{"Stop the angel", "netivri down"},
			{"Stop the angel of another profile", "netivri down --home ./work-profile"},
		},
		Errors:  []helpError{herr("ANGEL_STOP_TIMEOUT", cli.ExitInternal, "the angel did not stop within 15 seconds")},
		SeeAlso: []string{"up", "status"},
	},
	{
		Name:    "status",
		Group:   groupService,
		Summary: "Show the running angel",
		Usage:   []string{"netivri status [--home {DIR}] [--full] [--watch] [--interval {DURATION}]"},
		Description: "Reports the live state of the angel: listen address and protocols, authentication mode, the " +
			"allowed_clients rule, accounts " +
			"with their provider and exit country, uptime and version and, while accounts have routes, the warm-up " +
			"probes, the statistics store and the sticky-routing counters.\n\n" +
			"The 'activity' list shows who is using the proxy now: for every account whether it is active (has open " +
			"connections), how many connections and bytes, and through which nodes, providers and countries, the " +
			"busiest first; a node also carries the display name the subscription gives it, when there is one. " +
			"'active_accounts' counts the active ones.\n\n" +
			"It exits with 4 when no angel is running, so it works as a check in scripts. By default the answer is " +
			"short; --full adds the complete warm-up node list, the statistics store and the sticky-routing counters.\n\n" +
			"With --watch it keeps running and prints who is active, one frame per --interval (default 2s, between " +
			"1s and 60s). With --json it prints one JSON object per line instead. It ends with 4 when the angel " +
			"stops; --watch cannot be combined with --full.",
		Flags: []helpFlag{
			{"--full", "Print everything: the complete warm-up node list with the pool counters, the statistics store and the sticky-routing counters. Scripts that read warm.nodes, stats or affinity need it."},
			{"--watch", "Keep running and print who is active, one frame per --interval, until interrupted or the angel stops (exit 4)."},
			{"--interval {DURATION}", "How often --watch polls the angel (default 2s, at least 1s, at most 60s). Without --watch it is a usage error."},
		},
		Examples: []helpExample{
			{"Show the angel and who is using it (see activity)", "netivri status"},
			{"Show everything including the warm node list, stats and affinity", "netivri status --full"},
			{"Watch who is active, refreshed every two seconds", "netivri status --watch"},
			{"Watch as one JSON object per line, every five seconds", "netivri status --watch --interval 5s --json"},
			{"Start the angel only when it is not running (POSIX shell)", "netivri status >/dev/null 2>&1 || netivri up"},
		},
		Errors:  []helpError{herr("ANGEL_NOT_RUNNING", cli.ExitNoAngel, "nothing is running; start it with 'netivri up'")},
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
			"A running angel applies the new nodes to its accounts within a few seconds, and re-fetches every " +
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
		Usage:   []string{"netivri account {add|clone|list|show|set-country|set-provider|set-clients|set-socks5|rm} [arguments]"},
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
			"A running angel applies account changes by itself within a few seconds. A new or removed account or a " +
			"new password restarts its listener on the same port, so open connections end; a new country or " +
			"provider list keeps them. 'netivri status' shows the outcome under 'reload'.",
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
		Usage:   []string{"netivri account add {ACCOUNT} [--provider {SUBSCRIPTION}[,{SUBSCRIPTION}...]] [--country {COUNTRY}[,{COUNTRY}...]] [--pre-socks5 {URL}] [--post-socks5 {URL}]"},
		Description: "Creates a proxy login with the password placeholder 'init': the first client that connects with " +
			"this login sets the password. A login uses the characters A-Z a-z 0-9 . _ @ - (and is not 'init').\n\n" +
			"Repeating the command with the same providers and countries, in the same order, succeeds and reports " +
			"existing true; any difference for an existing login is ACCOUNT_EXISTS (use set-country or " +
			"set-provider to change it). " +
			"A running angel applies the new account within a few seconds. Optional --pre-socks5 and --post-socks5 set " +
			"account-specific proxy hops (socks5://, http:// or https://); see 'netivri help routing' for order and inheritance.",
		Flags: []helpFlag{
			{"--provider {SUBSCRIPTION}[,{SUBSCRIPTION}...]", "Route only through the nodes of this subscription, or of several, separated by commas and in priority order. Each must be a configured subscription."},
			{"--country {COUNTRY}[,{COUNTRY}...]", "Route only through nodes of this country: an ISO 3166-1 alpha-2 code such as DE, NL or FI, any case; or several codes, separated by commas and in priority order. * stands for every country not named in the list: DE,* is Germany first, then anywhere; * alone is any country. Quote it where the shell expands globs."},
			{"--clients {RULES}", "Client addresses that may use this account: an allowed-clients rule list (e.g. 10.0.0.0/8, 192.168.1.*, loopback, *). Default: no account restriction."},
			{"--pre-socks5 {URL}", "Proxy hop before the selected netivri exit. Use socks5://, http:// (an HTTP CONNECT proxy) or https:// (the same over TLS) as scheme://[user:password@]host:port; the port is required; credentials are never shown."},
			{"--post-socks5 {URL}", "Proxy hop after the selected netivri exit. Use socks5://, http:// (an HTTP CONNECT proxy) or https:// (the same over TLS) as scheme://[user:password@]host:port; the port is required; credentials are never shown."},
		},
		Examples: []helpExample{
			{"Exit through Germany, using the subscription main", "netivri account add alice --provider main --country DE"},
			{"Exit through the Netherlands, any subscription that has such nodes", "netivri account add bob --country NL"},
			{"Any country of one subscription", "netivri account add carol --provider main"},
			{"Germany first, then the Netherlands; main first, then backup", "netivri account add dave --provider main,backup --country DE,NL"},
			{"Connect: the first password you send for alice becomes her password", "curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org"},
			{"Only clients from 10.0.0.0/8 may use alice", "netivri account add alice --provider main --clients 10.0.0.0/8"},
			{"Reach the VPN node through a SOCKS5 proxy", "netivri account add alice --provider main --pre-socks5 socks5://pre.example:1080"},
			{"Send the traffic on to a SOCKS5 proxy after the VPN node", "netivri account add bob --provider main --post-socks5 socks5://post.example:1080"},
		},
		Errors: []helpError{
			herr("ACCOUNT_INVALID", cli.ExitUsage, "the login is not valid"),
			herr("COUNTRY_INVALID", cli.ExitUsage, "the country is not an ISO 3166-1 alpha-2 code"),
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "the provider is not a configured subscription"),
			herr("ACCOUNT_EXISTS", cli.ExitConflict, "the login exists with a different provider or country"),
			herr("CONFIG_INVALID", cli.ExitUsage, "--clients is not a valid allowed-clients value"),
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
		Description: "Creates {ACCOUNT} with the provider, exit country and allowed_clients of {SOURCE} and, unless " +
			"--password says " +
			"otherwise, with the same password: the stored hash is copied, so both logins accept the same password. " +
			"If {SOURCE} still has the placeholder 'init', so does the copy.\n\n" +
			"--password {PASSWORD} gives the new account a password of its own (1 to 1024 bytes, stored as an " +
			"argon2id hash); --password init leaves it to the first client that connects. --provider and --country " +
			"replace the copied route, each as in 'account add', and the new route must have stored nodes, otherwise " +
			"nothing is created. {SOURCE} is never changed. A running angel picks the new account up within a few " +
			"seconds.",
		Flags: []helpFlag{
			{"--password {PASSWORD}", "A new password for {ACCOUNT}. Default: copy the password of {SOURCE}."},
			{"--password init", "Use the placeholder: the first password a client sends for {ACCOUNT} becomes its password."},
			{"--provider {SUBSCRIPTION}[,...]", "Replace the copied provider (or list of providers, in priority order)."},
			{"--country {COUNTRY}[,...]", "Replace the copied exit country (or list, in priority order; * stands for every country not named)."},
			{"--clients {RULES}", "Replace the copied allowed-clients rule list. Clear it afterwards with 'account set-clients --none'."},
		},
		Examples: []helpExample{
			{"A second login with the same route and the same password", "netivri account clone alice bob"},
			{"The same route, a password of its own", "netivri account clone alice bob --password choose-a-password"},
			{"The same route, the first client sets the password", "netivri account clone alice bob --password init"},
			{"The same login settings, another country", "netivri account clone alice carol --password init --country NL"},
			{"Clone alice but restrict her to loopback clients", "netivri account clone alice bob --clients loopback"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "{SOURCE} does not exist"),
			herr("ACCOUNT_EXISTS", cli.ExitConflict, "{ACCOUNT} exists already (or is the login of the proxy authentication)"),
			herr("ACCOUNT_INVALID", cli.ExitUsage, "a login is not valid"),
			herr("COUNTRY_INVALID", cli.ExitUsage, "the country is not an ISO 3166-1 alpha-2 code"),
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "the provider is not a configured subscription"),
			herr("NO_NODES_FOR_COUNTRY", cli.ExitNotFound, "the replaced route has no stored nodes"),
			herr("CONFIG_INVALID", cli.ExitUsage, "--clients is not a valid allowed-clients value"),
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
		Description: "Shows each account: login, provider, exit_country, allowed_clients (empty when the account has " +
			"no rule of its own) and password_set, which is false until the " +
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
			"Per node the command shows its label ({PROVIDER}/{NODE_ID}), its display name when the subscription " +
			"gives one, provider, country and tier index and, " +
			"when the angel runs, live data: alive or down with the last probe delay, and the account's live " +
			"connections through the node. A (country, provider) pair without nodes is listed as a skipped tier " +
			"with a reason and does not consume a tier index, so the indexes match the order the angel actually " +
			"tries.\n\n" +
			"Without a running angel the static part is still shown and live is false (the live fields are null). " +
			"SOCKS5 hops are shown as effective pre_socks5 and post_socks5 values with their source (account or global); " +
			"credentials are redacted. Passwords, hashes and subscription URLs are never shown.",
		Examples: []helpExample{
			{"Show alice and her route tiers", "netivri account show alice"},
			{"While the angel runs: alive, delay and live connections per node", "netivri account show alice"},
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
			"A running angel applies the change within a few seconds.\n\n" +
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
			"An account whose value does not change is not checked. A running angel applies the change within a few " +
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
		Name:    "account set-socks5",
		Group:   groupAccount,
		Summary: "Set or inherit account proxy hops",
		Usage: []string{
			"netivri account set-socks5 {ACCOUNT}[,{ACCOUNT}...] --pre {URL|none|inherit}",
			"netivri account set-socks5 {ACCOUNT}[,{ACCOUNT}...] --post {URL|none|inherit}",
			"netivri account set-socks5 {ACCOUNT}[,{ACCOUNT}...] --pre {VALUE} --post {VALUE}",
		},
		Description: "Updates one or more accounts' chain sides. Set a side to a socks5://, http:// or https:// " +
			"user:password@host:port URL (port required; http is an HTTP CONNECT proxy, https the same over TLS), none to explicitly disable that hop, or inherit to use the global chain setting. " +
			"Unspecified sides are unchanged. The sides are independent; URLs are credentials and are never printed. " +
			"Changes are applied by a running angel within a few seconds.",
		Flags: []helpFlag{
			{"--pre {VALUE}", "Set the pre-hop proxy URL (socks5://, http:// or https://; port required), or none/inherit."},
			{"--post {VALUE}", "Set the post-hop proxy URL (socks5://, http:// or https://; port required), or none/inherit."},
		},
		Examples: []helpExample{
			{"Set a pre-hop", "netivri account set-socks5 alice --pre socks5://user:secret@proxy.example:1080"},
			{"Set an HTTP proxy as the post-hop", "netivri account set-socks5 alice --post http://proxy.example:8080"},
			{"Disable the inherited post-hop", "netivri account set-socks5 alice --post none"},
			{"Return to the global pre-hop", "netivri account set-socks5 alice --pre inherit"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "an account in the list does not exist"),
			herr("USAGE", cli.ExitUsage, "requires one or more account logins and --pre and/or --post"),
		},
		SeeAlso: []string{"account add", "account show", "config", "routing"},
	},
	{
		Name:    "account set-clients",
		Group:   groupAccount,
		Summary: "Change or clear the client addresses that may use an account",
		Usage: []string{
			"netivri account set-clients {ACCOUNT}[,{ACCOUNT}...] {RULES}",
			"netivri account set-clients --all {RULES}",
			"netivri account set-clients {ACCOUNT}[,{ACCOUNT}...] --none",
		},
		Description: "Sets which client addresses may use existing accounts, or clears the restriction with --none. " +
			"The value is one comma-separated allowed-clients rule list ('netivri help config' has the full " +
			"grammar: * , loopback, exact addresses, IPv4 whole-octet masks like 192.168.1.*, CIDR). {ACCOUNT} is " +
			"the login of an account, for example alice; several logins are separated by commas, with no spaces, " +
			"and --all means every account.\n\n" +
			"The account rule only narrows what the global listen.allowed_clients already allows; a client allowed by " +
			"neither is refused before the password is verified, so an 'init' account cannot be claimed from a " +
			"disallowed address. A running angel applies the change within a few seconds.\n\n" +
			"With one login the result is {\"account\": ...}; with several logins or --all it is {\"accounts\": [...]}.",
		Flags: []helpFlag{
			{"--all", "Change every account instead of naming them."},
			{"--none", "Clear the rule instead of setting one."},
		},
		Examples: []helpExample{
			{"Only loopback clients may use alice", "netivri account set-clients alice loopback"},
			{"Restrict every account to one network", "netivri account set-clients --all '10.0.0.0/8'"},
			{"Let every client use alice again", "netivri account set-clients alice --none"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "an account in the list does not exist, or --all with no accounts"),
			herr("ACCOUNT_INVALID", cli.ExitUsage, "a login in the list is not valid"),
			herr("CONFIG_INVALID", cli.ExitUsage, "the rule list is not a valid allowed-clients value"),
		},
		SeeAlso: []string{"account set-country", "account set-provider", "account set-socks5", "config"},
	},
	{
		Name:    "account rm",
		Group:   groupAccount,
		Summary: "Remove an account",
		Usage:   []string{"netivri account rm {ACCOUNT}"},
		Description: "Removes the account. It is not idempotent. A running angel applies the removal within a few " +
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
		Description: "Shows what the angel recorded about the exit nodes: the health and delay from the background " +
			"probes, the dial time and outcome of real connections, and the throughput of real traffic. It reads " +
			"{DIR}/stats.db read-only, so it works while the angel runs and after it stopped. Statistics exist " +
			"only for accounts that have a provider or an exit country. The per-node view also shows the display " +
			"name of each node, when the subscription gives one; grouped views do not.\n\n" +
			"The angel also records per account which nodes it used, the dial outcomes and the traffic " +
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
		Summary: "Show the angel log",
		Usage:   []string{"netivri logs [--follow] [--level {debug|info|warn|error}] [--since {DURATION}] [--tail {N}] [--home {DIR}]"},
		Description: "Shows what the angel wrote to {DIR}/run/netivri.log: start and stop, applied and rejected " +
			"configuration changes, subscription refreshes, storage problems and route nodes going down (with the " +
			"reason) or coming back (with the downtime), each with a time and a level. Three or more nodes " +
			"going down within 10 seconds are one 'node burst' warning, and a node that goes down 3 times within an " +
			"hour is one 'flapping' warning, not a warning per drop; the repeats of nodes no account uses are DEBUG " +
			"lines, shown only with --level debug. Once a minute it also sums up what went wrong for the " +
			"connections of an account (failed dials, connections broken by their node, sites moved to another node, " +
			"failed proxy logins), without sites, addresses, error texts or passwords. It " +
			"reads the file, so it works while the angel runs and after it stopped or failed to start. Passwords, " +
			"tokens and subscription URLs are never logged.\n\n" +
			"Without --follow the result is one document with the newest entries. With --follow the command keeps " +
			"running and prints new entries as lines until Ctrl+C; with --json each line is one JSON object (NDJSON).",
		Flags: []helpFlag{
			{"--follow", "Keep running and print new entries. Waits for the log when the angel has not started yet."},
			{"--level {LEVEL}", "Only this level and above: debug, info, warn or error. Default: all."},
			{"--since {DURATION}", "Only the last period: 90m, 24h, 7d and so on, at most 365d. Default: no limit."},
			{"--tail {N}", "At most the newest N entries (default 100, 0 for all). With --follow it limits the backlog shown first."},
		},
		Examples: []helpExample{
			{"The newest entries", "netivri logs"},
			{"Problems of the last day", "netivri logs --level warn --since 24h"},
			{"Watch the angel live", "netivri logs --follow"},
			{"Stream to a script, one JSON object per line", "netivri logs --follow --json"},
		},
		Errors: []helpError{
			herr("LOG_NOT_FOUND", cli.ExitNotFound, "the angel has not written a log yet"),
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
			"Give exactly one of --account and --node. It runs in its own process, without the angel, and sends a " +
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
		Name:    "doctor",
		Group:   groupDiag,
		Summary: "One-pass diagnosis of the setup, with an exit code",
		Usage:   []string{"netivri doctor [--home {DIR}]"},
		Description: "Runs every check once and prints one document on stdout: checks, each with a " +
			"name, a status (ok, warn or fail) and a detail, plus a summary count. When a check " +
			"failed, the error envelope goes to stderr and the exit code is 7, so a script can run " +
			"'netivri doctor' and branch on the exit code alone.\n\n" +
			"The checks: config (config.ktav loads and is valid), angel (an angel runs for this " +
			"state directory), listen (the address the running angel reports, or, when nothing " +
			"runs, whether the configured port is free), subscriptions (every configured " +
			"subscription has a stored snapshot no older than twice fetch.update_interval, skipped " +
			"when the interval is off, and every account names configured subscriptions only), one " +
			"check per account (its route has stored nodes and, with a running angel, a live node " +
			"in the first tier that has nodes; without an angel the stored nodes are counted and " +
			"the liveness part is a warn) and log (no WARN or ERROR entries in the last hour of " +
			"the angel log; reports of nodes going down (bursts and flapping nodes included) are only counted, since the account " +
			"checks cover the routes; the log is read even when the angel is down, and a missing " +
			"log file is fine).\n\n" +
			"A check that cannot run is a warn with the reason, never a failure of the whole " +
			"command. No check contacts anything but the local angel and the local port; " +
			"subscription URLs, passwords and hashes are never printed.",
		Examples: []helpExample{
			{"Check the whole setup; the exit code says whether something failed", "netivri doctor"},
			{"For a script or an agent", "netivri doctor --json"},
		},
		Errors: []helpError{
			herr("DOCTOR_FAILED", cli.ExitCheckFailed, "at least one check failed; the document on stdout says which"),
		},
		Notes:   []string{"A warn means something to look at; only a fail makes the exit code 7."},
		SeeAlso: []string{"status", "stats", "diag dial", "logs"},
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

	// Concept topics: not commands, but everything an operator with only
	// the binary needs in order to configure and run netivri.
	{
		Name:    "config",
		Group:   groupConcepts,
		Summary: "The config.ktav file and every key it accepts",
		Usage:   []string{"netivri help config"},
		Description: "netivri keeps its settings in config.ktav inside the state directory: the directory given by " +
			"--home {DIR} or $NETIVRI_HOME, else the user config directory plus /netivri (%APPDATA%\\netivri on " +
			"Windows, ~/.config/netivri on Linux, ~/Library/Application Support/netivri on macOS). A missing file " +
			"is not an error: safe defaults apply (proxy on 127.0.0.1:7890, every client address allowed, no " +
			"authentication, manual selection). Every command that changes settings writes this file; a running " +
			"angel notices and applies the change by itself within a few seconds.\n\n" +
			"The file is Ktav, a plain-text key/value format, and safe to edit by hand. But netivri rewrites the " +
			"whole file whenever it persists a change, so hand-written comments are not preserved; keep notes in " +
			"a separate file.\n\n" +
			"The keys: listen.bind is the address the proxy binds (default 127.0.0.1), listen.port the port " +
			"(default 7890, 1 to 65535), listen.allowed_clients which client addresses may connect (see below). " +
			"auth.login and auth.password turn proxy authentication on; either both or neither. auth.password is " +
			"either the placeholder init or an argon2id hash (it starts with $argon2id$); a plain password is " +
			"rejected. auth.allow_init_remote (default false) permits the init placeholder on a non-loopback bind. " +
			"fetch.proxy is the proxy for all outgoing subscription fetches, a socks5://, socks5h://, http:// or " +
			"https:// URL with a host. fetch.update_interval is how often the angel re-fetches every " +
			"subscription: a duration from 5m to 168h, or off; the default is 1h.\n\n" +
			"subscriptions is an array of provider subscriptions, each with subscriptions.name, subscriptions.url " +
			"(a secret, never printed) and an optional subscriptions.via, a per-subscription fetch proxy with the " +
			"same rules as fetch.proxy. accounts is an array of proxy accounts: accounts.login (1 to 64 " +
			"characters of A-Za-z0-9._@-, unique, not the reserved init, not a duplicate of a non-init " +
			"auth.login), accounts.password (init or an argon2id hash), and optional accounts.provider (a " +
			"comma-separated list of subscription names in priority order), accounts.exit_country (a " +
			"comma-separated list of two-letter country codes, * allowed) and accounts.allowed_clients (the " +
			"account's own client-address rule). selection.node, selection.country and selection.mode (manual, " +
			"fastest or auto; " +
			"default manual) set the preferred exit.\n\n" +
			"chain.pre_socks5 and chain.post_socks5 are optional global hop defaults; despite the names they take " +
			"socks5://, http:// (an HTTP CONNECT proxy) or https:// (the same over TLS) URLs. Each account may " +
			"set accounts.pre_socks5 and accounts.post_socks5; each side cascades independently: absent/empty or inherit uses the " +
			"global value, while none explicitly disables that hop. Hop URLs require a port (1 to 65535) " +
			"and may include optional percent-encoded username:password userinfo; these are credentials and are " +
			"never printed. See 'netivri help routing'.\n\n" +
			"listen.allowed_clients and accounts[].allowed_clients are a comma-separated list of rules; spaces " +
			"around a comma are ignored. The rules: * (every address), loopback (127.0.0.0/8 and ::1), an exact " +
			"IPv4 or IPv6 address, an IPv4 whole-octet mask (trailing * octets only: 192.168.1.*, 10.*; a * " +
			"anywhere else, such as 192.*.1.5, is an error - use a CIDR prefix) and a CIDR prefix (10.0.0.0/8, " +
			"fd00::/8). At most 64 rules and 1024 bytes in total. The value is checked against the client " +
			"address before any handshake, so a refused connection is closed without a reply. The global rule and " +
			"the account rule cascade: the account rule can only narrow what the global rule already allows. An " +
			"absent listen.allowed_clients means *; a value that is present but empty, including the raw-string " +
			"spelling listen.allowed_clients::, is a CONFIG_INVALID error naming the key.\n\n" +
			"The init placeholder means the first client that authenticates with that login sets the password: " +
			"the angel stores its argon2id hash in the file. init is accepted only when listen.bind is a " +
			"loopback address, unless auth.allow_init_remote is true, and a client from an address the " +
			"allowed_clients rules refuse can never claim it.",
		Examples: []helpExample{
			{"A complete config.ktav; the :: marker keeps a value from being read as a number or boolean", "" +
				"listen.bind: 127.0.0.1\n" +
				"listen.port: 7890\n" +
				"auth.login:: init\n" +
				"auth.password:: init\n" +
				"auth.allow_init_remote: false\n" +
				"subscriptions: [\n" +
				"    {\n" +
				"        name: main\n" +
				"        url:: https://provider.example/sub/SECRET\n" +
				"    }\n" +
				"]\n" +
				"accounts: [\n" +
				"    {\n" +
				"        login: alice\n" +
				"        password:: init\n" +
				"        provider: main\n" +
				"        exit_country: DE\n" +
				"    }\n" +
				"]\n" +
				"selection.node::\n" +
				"selection.country::\n" +
				"selection.mode: manual"},
			{"Limit the proxy to your local network", "listen.allowed_clients: loopback,192.168.1.*"},
			{"Full reference for every key", "https://github.com/PHPCraftdream/netivri/blob/main/docs/config.md"},
		},
		SeeAlso: []string{"routing", "up", "account add"},
	},
	{
		Name:    "routing",
		Group:   groupConcepts,
		Summary: "How a connection picks its exit node: tiers, fallback, sticky bindings",
		Usage:   []string{"netivri help routing"},
		Description: "An account with a provider list or an exit-country list is routed through subscription nodes, " +
			"never directly. The exit_country list gives the countries first in list order; inside a country the " +
			"providers of the account follow in their order. A tier is the nodes of one (country, provider) pair, " +
			"and a new connection goes to a node of the first tier that has one up, so a later tier is only a " +
			"fallback. There is no fallback beyond the lists: no direct connection, and no country or provider " +
			"the account does not name. An account with neither a provider nor a country uses the default route, " +
			"which is direct until a node is selected.\n\n" +
			"A chain has up to three hops: an optional pre_socks5 hop, the selected subscription exit, " +
			"and an optional post_socks5 hop. The order is client -> pre-hop -> selected exit -> post-hop -> target; " +
			"each hop URL (socks5://, http:// or https://) requires a port. Account pre/post settings cascade independently: inherit uses the " +
			"corresponding global chain setting, while none disables it. Credentials in URLs are redacted in help " +
			"output and account show. Use 'netivri help config' for configuration keys and 'netivri help account " +
			"set-socks5' to change account hops.\n\n" +
			"A * in the country list stands for every country the list does not name, nodes of an unknown country " +
			"included, at its place in the order: DE,* tries Germany first, then everything else; * alone is any " +
			"country. Quote it in shells that expand globs (--country '*').\n\n" +
			"The binding between an account and a site is sticky: once the angel picked an exit node for the " +
			"(login, site) pair it keeps that node for later connections to the same site while the node is up, " +
			"until a node of a better tier has been up for 5 minutes, when the next connection of the site goes " +
			"back to that tier (open connections stay). The bindings survive restarts and configuration reloads: the " +
			"angel stores them in stats.db and loads them back when it starts or reloads. 'netivri help status' " +
			"describes the affinity counters.\n\n" +
			"The angel probes the route nodes in the background. A node is marked down after 2 consecutive " +
			"failed probes or 3 consecutive failed dials to at least 2 different sites (one unreachable " +
			"destination is not the node's fault) or 10 to one site; a failed probe does not count while data has come back " +
			"through the node within the last minute. A single failed dial is returned to the client as an " +
			"error and the client retries; the connection is not moved on the spot, unless the node just went " +
			"down with that failure, in which case the angel re-binds and retries once on the node it now " +
			"selects. Once a node is down, new connections skip it and existing bindings to it re-bind to the " +
			"next up node or tier; the node comes back as soon as a probe or a dial succeeds. A down node is " +
			"probed with growing pauses: the probe interval doubles twice (base, 2x, 4x) and then caps.\n\n" +
			"An account is created with the password placeholder init: the first client that authenticates with " +
			"that login claims it, and the angel stores the argon2id hash of the password it sent. A client from " +
			"an address the allowed_clients rules refuse is turned away before the password is checked, so a " +
			"disallowed address can never claim an init password.\n\n" +
			"The client-address rules cascade: the global listen.allowed_clients is checked first, then the " +
			"account's own allowed_clients. The account rule can only narrow what the global rule already " +
			"allows, never widen it.",
		Examples: []helpExample{
			{"The route of an account, tier by tier", "netivri account show alice"},
			{"Give an account providers and countries in priority order", "netivri account add alice --provider main,backup --country DE,NL"},
			{"Full reference", "https://github.com/PHPCraftdream/netivri/blob/main/docs/cli.md"},
		},
		SeeAlso: []string{"config", "account show", "account set-country", "account set-socks5", "status"},
	},
	{
		Name:    "output",
		Group:   groupConcepts,
		Summary: "The fields each command prints",
		Usage:   []string{"netivri help output"},
		Description: "Every command prints one document with schema_version, ok and data, in Ktav by default and as " +
			"one JSON object with --json; a failure prints the same shape on stderr with a stable error code. " +
			"'netivri help {COMMAND}' gives the document of one command; this topic lists the fields of the " +
			"data objects. Subscription URLs, passwords and hashes are never printed.",
		Examples: []helpExample{
			{"The document of one command, as JSON for a script", "netivri help status --json"},
			{"Full reference", "https://github.com/PHPCraftdream/netivri/blob/main/docs/cli.md"},
		},
		Notes: []string{
			"status: angel, foreground, pid, listen, protocols, auth, allowed_clients, accounts[] (login, " +
				"provider, exit_country, allowed_clients), route, started_at, uptime_seconds, version, " +
				"active_accounts, activity[] (login, active, connections, upload_bytes, download_bytes - byte counts -, " +
				"providers[], countries[], nodes[] with node, name, provider, country, connections and the byte " +
				"counters), warm (alive = nodes whose last probe succeeded, total = the number of nodes, " +
				"probes = probes sent so far, probe_failures = failed probes), refresh (interval_seconds = 0 " +
				"when the refresh is off, ok, failed, last_at, last_error) and reload (count = core restarts " +
				"caused by a changed configuration, last_at, last_error). Timestamps are RFC3339 UTC. --full " +
				"adds the complete warm with nodes[] (name = the internal proxy name, alive, delay_ms, 0 until " +
				"a probe succeeded, label, provider, country, node_name), stats (path, dropped = samples lost " +
				"to a full write queue) and affinity (bindings = current site-to-node bindings held, rebinds = " +
				"how often a binding moved to another node because it was down, returns = how often a binding went " +
				"back to a better tier, hedges = hedge dial attempts started, hedge_wins = " +
				"dials won by the hedge attempt, restored = persisted bindings loaded at the latest core start " +
				"or reload, saved_at = RFC3339 UTC of the last successful bindings write, omitted when none). " +
				"--watch prints one frame per interval: time, active_accounts, accounts[] (login, active, " +
				"connections, up_bytes_per_s, down_bytes_per_s - bytes per second, null on the first frame) " +
				"and nodes[] (node, name, provider, country, connections, byte counters).",
			"stats: since, by, items[]. Timestamps are RFC3339 UTC (last_seen and last_ok are null when never). Percent fields run 0 to 100 with one " +
				"decimal and are null when nothing was measured: uptime_pct is successful probes over all " +
				"probes of the window, dial_ok_pct successful dials over all dials. Per node (by node): " +
				"label, name (only when the subscription gives a display name), provider, country, probes, " +
				"uptime_pct, delay_ms (avg, p50, p95 - milliseconds of probe delays), dials, dial_ok_pct, " +
				"dial_ms_avg (mean milliseconds of successful dials), throughput_kbps (p50, max - KiB/s per " +
				"throughput window), bytes (total bytes moved in the window), last_seen, last_ok. By country " +
				"or provider the group name stands under the --by key and percentiles are left out. By " +
				"account: login, bytes_up, bytes_down (byte counts), dials, dial_ok_pct, nodes (a count), countries, " +
				"providers (comma-joined).",
			"doctor: checks[] (name, status - ok, warn or fail - and detail, omitted when empty) plus " +
				"summary (the ok, warn and fail counts). Exit code 7 when a check failed.",
			"account show: login, provider, exit_country (the raw configured lists), providers and countries " +
				"(the parsed lists in priority order, * included), tiers[] and live (true when the angel " +
				"answered). tiers[]: tier (the index; a tier without nodes is marked skipped true, carries a " +
				"reason and keeps the index it would have had, so it consumes no tier number), country, " +
				"providers[], nodes[] (label, name - only when the subscription gives a display name -, " +
				"provider, country, tier, alive, delay_ms, connections). Without the angel, alive, delay_ms " +
				"and connections are null. With it, connections is the live connection count when the angel tracks the " +
				"account (0 for an idle node) and null when it does not track it; alive and delay_ms are null unless " +
				"the angel's warm-up list contains the node's label.",
			"logs: entries[] (time - RFC3339 UTC -, level, message), oldest first. With --follow the output " +
				"is a stream: log lines, or with --json one object per line (time, level, message).",
			"sub list: subscriptions[] (name, host - of the URL; the URL itself is never printed -, via_set " +
				"(a via proxy is set), nodes (a count, null until the first fetch), fetched_at (RFC3339 UTC, " +
				"null until the first fetch), quota). quota is null when the provider sent no " +
				"subscription-userinfo header; otherwise used (upload + download, bytes), total (bytes, null " +
				"when the provider gives none) and expire (RFC3339 UTC, null when the provider gives none).",
			"node list: nodes[] (id, name - the node name as the provider gave it -, type, server, port, country (read " +
				"from the node name, a hint), sub) and count.",
		},
		SeeAlso: []string{"status", "stats", "account show", "sub list", "node list", "logs", "doctor"},
	},
}
