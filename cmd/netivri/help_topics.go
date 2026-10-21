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
	"one JSON object, so it works the same in a terminal, in scripts and for AI agents."

var helpOverviewUsage = []string{
	"netivri <command> [arguments] [flags]",
	"netivri help [command]",
}

var helpQuickStart = []string{
	"# 1. add a subscription (the URL is a credential, keep it private)",
	"netivri sub add https://provider.example/sub/<token> --name main",
	"# 2. create a login that exits through Germany",
	"netivri account add alice --provider main --country DE",
	"# 3. start the proxy in the background and check it",
	"netivri up",
	"netivri status",
	"# 4. use it; the first password you send as alice becomes her password",
	"curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org",
}

var helpGlobalFlags = []helpFlag{
	{"--home DIR", `State directory with config.ktav, subscriptions/, run/ and stats.db. ` +
		`Default: $NETIVRI_HOME, else the user config directory plus /netivri ` +
		`(%APPDATA%\netivri on Windows, ~/.config/netivri on Linux, ` +
		`~/Library/Application Support/netivri on macOS).`},
	{"--json", "Accepted by every command. Results are always one JSON object today; for help it selects the machine-readable form."},
	{"--quiet", "Accepted for compatibility; changes nothing today."},
	{"--no-color", "Accepted for compatibility; there is no color output."},
	{"-h, --help", "Show help for a command, for example: netivri up --help"},
}

const helpOutput = `Every command prints one JSON object on stdout: ` +
	`{"schema_version":1,"ok":true,"data":{...}}. A failure prints one JSON object on stderr ` +
	`with a stable "code" and ends with a non-zero exit code; branch on the code, not on the ` +
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
var helpPlanned = []string{"use", "node test", "logs", "doctor", "schema"}

func herr(code string, exit int, meaning string) helpError {
	return helpError{Code: code, Exit: exit, Meaning: meaning}
}

var helpTopics = []helpTopic{
	{
		Name:    "up",
		Group:   groupService,
		Summary: "Start the proxy (a background daemon by default)",
		Usage:   []string{"netivri up [--port N] [--bind ADDR] [--foreground] [--home DIR]"},
		Description: "Starts the local proxy: SOCKS5, SOCKS4/4a and HTTP (plain requests and CONNECT) on one TCP port, " +
			"told apart by the first byte of each connection. Without --foreground it starts a detached background " +
			"daemon and returns at once; running it again is safe and reports the running instance.\n\n" +
			"Settings come from config.ktav in the state directory; --port and --bind override listen.port and " +
			"listen.bind. An account with a provider or an exit country is routed only through matching nodes, " +
			"never directly. UDP is not relayed.",
		Flags: []helpFlag{
			{"--port N", "Local port, default 7890. 0 takes any free port; the chosen address is in the output (listen)."},
			{"--bind ADDR", "Address to listen on, default 127.0.0.1. Anything but loopback needs proxy authentication in config.ktav, otherwise up fails with AUTH_REQUIRED."},
			{"--foreground", "Run in this terminal until Ctrl+C or 'netivri down'. Prints one JSON line when ready. Fails with ALREADY_RUNNING when a daemon is running."},
			{"--home DIR", "State directory (see the global flags in 'netivri help')."},
		},
		Examples: []helpExample{
			{"Start in the background on 127.0.0.1:7890", "netivri up"},
			{"Take any free port and read it from the output", "netivri up --port 0"},
			{"Run in the foreground, for example under a service manager", "netivri up --foreground"},
			{"Keep a separate profile with its own config, subscriptions and statistics", "netivri up --home ./work-profile"},
			{"Listen on every interface (needs auth.login and auth.password in config.ktav)", "netivri up --bind 0.0.0.0"},
			{"Apply new accounts or a changed config: restart", "netivri down\nnetivri up"},
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
		Usage:   []string{"netivri down [--home DIR]"},
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
		Usage:   []string{"netivri status [--home DIR]"},
		Description: "Reports the live state of the daemon: listen address and protocols, authentication mode, accounts " +
			"with their provider and exit country, uptime and version and, while accounts have routes, the warm-up " +
			"probes, the statistics store and the sticky-routing counters.\n\n" +
			"It exits with 4 when no daemon is running, so it works as a check in scripts.",
		Examples: []helpExample{
			{"Show the daemon", "netivri status"},
			{"Start the daemon only when it is not running (POSIX shell)", "netivri status >/dev/null 2>&1 || netivri up"},
		},
		Errors:  []helpError{herr("DAEMON_NOT_RUNNING", cli.ExitNoDaemon, "nothing is running; start it with 'netivri up'")},
		SeeAlso: []string{"up", "stats"},
	},

	{
		Name:    "sub",
		Summary: "Manage provider subscriptions",
		Usage:   []string{"netivri sub <add|list|update|rm|countries> [arguments]"},
		Description: "A subscription is the link a provider gives you; it lists proxy nodes as Clash/Mihomo YAML or as " +
			"a base64 list of share links (vless://, vmess://, trojan://, ss://, hysteria2://). The URL is a credential: " +
			"netivri never prints or logs it. Every fetch goes through a strict proxy path and never silently directly.",
		Examples: []helpExample{
			{"Add a subscription, look at it and refresh it later", "netivri sub add https://provider.example/sub/<token> --name main\nnetivri sub list\nnetivri sub update --all"},
		},
		SeeAlso: []string{"account", "node list"},
	},
	{
		Name:    "sub add",
		Group:   groupSubs,
		Summary: "Add a subscription",
		Usage:   []string{"netivri sub add <url> [--name NAME] [--via PROXY]"},
		Description: "Fetches the subscription, parses it, stores its nodes and only then adds it to the config. If " +
			"anything fails nothing is added. The fetch goes through --via when given, otherwise through fetch.proxy " +
			"from config.ktav, otherwise directly; environment proxies are never used.\n\n" +
			"It is idempotent by URL: adding a known URL again re-fetches it under its existing name and reports " +
			"existing true. The URL is never printed.",
		Flags: []helpFlag{
			{"--name NAME", "Name of the subscription (letters, digits, . _ -; at most 64). Default: derived from the host of the URL. A name used by a different URL is SUB_EXISTS_NAME."},
			{"--via PROXY", "Fetch through this proxy: an http(s):// or socks5(h):// URL."},
		},
		Examples: []helpExample{
			{"Add a subscription; the name comes from the host", "netivri sub add https://provider.example/sub/<token>"},
			{"Give it a name you will use in accounts", "netivri sub add https://provider.example/sub/<token> --name main"},
			{"The provider is blocked directly: fetch through a local proxy", "netivri sub add https://provider.example/sub/<token> --name main --via socks5h://127.0.0.1:1080"},
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
		Usage:   []string{"netivri sub list [--home DIR]"},
		Description: "Shows each subscription: its name, the host of its URL, whether a --via proxy is set, how many " +
			"nodes are stored and when they were fetched (null until the first fetch). The URL and the proxy are never " +
			"printed.",
		Examples: []helpExample{{"List the subscriptions", "netivri sub list"}},
		SeeAlso:  []string{"sub update", "sub rm"},
	},
	{
		Name:    "sub update",
		Group:   groupSubs,
		Summary: "Re-fetch subscriptions",
		Usage:   []string{"netivri sub update [NAME | --all]"},
		Description: "Fetches subscriptions again and replaces their stored nodes. With a NAME only that subscription, " +
			"with --all or no argument every one, in config order. A subscription that fails keeps its old nodes. When " +
			"any fails, the results are still printed and the command ends with SUB_UPDATE_FAILED.\n\n" +
			"Running accounts keep their nodes until 'netivri down' and 'netivri up'.",
		Flags: []helpFlag{{"--all", "Update every subscription (the default without a NAME)."}},
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
		Usage:   []string{"netivri sub rm NAME"},
		Description: "Removes the subscription from the config and deletes its stored nodes. It is not idempotent. " +
			"It refuses while an account uses the subscription as its provider; free the account first with " +
			"'netivri account set-provider LOGIN --none'.",
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
			"netivri sub countries [NAME]",
			"netivri sub countries --url URL [--via PROXY]",
		},
		Description: "Shows which exit countries a provider offers, so you know what to give --country. With a NAME " +
			"it reads that provider's stored nodes without any network. With no argument it lists every provider and " +
			"the union of their countries. With --url it fetches a subscription through the same strict path as " +
			"'sub add' and prints its countries without storing anything.\n\n" +
			"A node's country is read from its name; nodes where that is not possible are counted as unknown.",
		Flags: []helpFlag{
			{"--url URL", "Count the countries of this subscription URL without storing it. Cannot be combined with a NAME."},
			{"--via PROXY", "Fetch --url through this proxy. Needs --url."},
		},
		Examples: []helpExample{
			{"Every provider and all countries", "netivri sub countries"},
			{"One provider", "netivri sub countries main"},
			{"Look before you add", "netivri sub countries --url https://provider.example/sub/<token>"},
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
		Usage:       []string{"netivri node list [--sub NAME] [--country CC]"},
		Description: "Nodes are the proxy servers listed by your subscriptions.",
		Examples:    []helpExample{{"List every stored node", "netivri node list"}},
		SeeAlso:     []string{"sub add"},
	},
	{
		Name:    "node list",
		Group:   groupSubs,
		Summary: "List the stored nodes",
		Usage:   []string{"netivri node list [--sub NAME] [--country CC]"},
		Description: "Prints the stored nodes of every subscription, or of one, in subscription order, with their id, " +
			"name, type, server, port and country. The country is read from the node name, so treat it as a hint " +
			"about the exit, not as proof. Credentials are never printed.",
		Flags: []helpFlag{
			{"--sub NAME", "Only the nodes of this subscription."},
			{"--country CC", "Only nodes of this country: an ISO 3166-1 alpha-2 code, any case."},
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
		Usage:   []string{"netivri account <add|list|set-country|set-provider|rm> [arguments]"},
		Description: "An account is a proxy login. Several accounts share one port. Each may name a provider (a " +
			"subscription) and an exit country, and its traffic leaves only through matching nodes, with no fallback " +
			"to a direct connection or to another country. An account with neither uses the default route, which is " +
			"direct.\n\n" +
			"A new account has the password placeholder 'init': the first client that connects with the login sets the " +
			"password, which is stored as an argon2id hash.\n\n" +
			"Account changes take effect after 'netivri down' and 'netivri up'.",
		Examples: []helpExample{
			{"Create a login, list the logins, restart to apply", "netivri account add alice --provider main --country DE\nnetivri account list\nnetivri down\nnetivri up"},
		},
		SeeAlso: []string{"up", "sub countries"},
	},
	{
		Name:    "account add",
		Group:   groupAccount,
		Summary: "Create an account",
		Usage:   []string{"netivri account add LOGIN [--provider NAME] [--country CC]"},
		Description: "Creates a proxy login with the password placeholder 'init': the first client that connects with " +
			"this login sets the password. A login uses the characters A-Z a-z 0-9 . _ @ - (and is not 'init').\n\n" +
			"Repeating the command with the same provider and country succeeds and reports existing true; any " +
			"difference for an existing login is ACCOUNT_EXISTS (use set-country or set-provider to change it). " +
			"Takes effect after 'netivri down' and 'netivri up'.",
		Flags: []helpFlag{
			{"--provider NAME", "Route only through the nodes of this subscription. It must be a configured subscription."},
			{"--country CC", "Route only through nodes of this country: an ISO 3166-1 alpha-2 code such as DE, NL or FI, any case."},
		},
		Examples: []helpExample{
			{"Exit through Germany, using the subscription main", "netivri account add alice --provider main --country DE"},
			{"Exit through the Netherlands, any subscription that has such nodes", "netivri account add bob --country NL"},
			{"Any country of one subscription", "netivri account add carol --provider main"},
			{"Connect: the first password you send for alice becomes her password", "curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org"},
		},
		Errors: []helpError{
			herr("ACCOUNT_INVALID", cli.ExitUsage, "the login is not valid"),
			herr("COUNTRY_INVALID", cli.ExitUsage, "the country is not an ISO 3166-1 alpha-2 code"),
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "the provider is not a configured subscription"),
			herr("ACCOUNT_EXISTS", cli.ExitConflict, "the login exists with a different provider or country"),
		},
		SeeAlso: []string{"account list", "sub countries", "up"},
	},
	{
		Name:    "account list",
		Group:   groupAccount,
		Summary: "List accounts",
		Usage:   []string{"netivri account list [--home DIR]"},
		Description: "Shows each account: login, provider, exit_country and password_set, which is false until the " +
			"first client claims the 'init' placeholder. Passwords and hashes are never shown.",
		Examples: []helpExample{{"List the accounts", "netivri account list"}},
		SeeAlso:  []string{"account add", "status"},
	},
	{
		Name:    "account set-country",
		Group:   groupAccount,
		Summary: "Change or clear the exit country of an account",
		Usage: []string{
			"netivri account set-country LOGIN CC",
			"netivri account set-country LOGIN --none",
		},
		Description: "Sets the exit country of an existing account, or clears it with --none. Repeating the same value " +
			"succeeds. Takes effect after 'netivri down' and 'netivri up'.",
		Flags: []helpFlag{{"--none", "Clear the country instead of setting one."}},
		Examples: []helpExample{
			{"Move alice to the Netherlands", "netivri account set-country alice NL"},
			{"Let alice use every country of her provider", "netivri account set-country alice --none"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "no account with that login"),
			herr("COUNTRY_INVALID", cli.ExitUsage, "the country is not an ISO 3166-1 alpha-2 code"),
		},
		SeeAlso: []string{"account set-provider", "sub countries"},
	},
	{
		Name:    "account set-provider",
		Group:   groupAccount,
		Summary: "Change or clear the provider of an account",
		Usage: []string{
			"netivri account set-provider LOGIN NAME",
			"netivri account set-provider LOGIN --none",
		},
		Description: "Binds an existing account to a configured subscription, or clears the binding with --none. " +
			"Repeating the same value succeeds. Takes effect after 'netivri down' and 'netivri up'.",
		Flags: []helpFlag{{"--none", "Clear the provider instead of setting one."}},
		Examples: []helpExample{
			{"Route alice through the subscription main", "netivri account set-provider alice main"},
			{"Free alice from her provider, for example before 'sub rm'", "netivri account set-provider alice --none"},
		},
		Errors: []helpError{
			herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "no account with that login"),
			herr("SUB_NOT_FOUND", cli.ExitNotFound, "the provider is not a configured subscription"),
		},
		SeeAlso: []string{"account set-country", "sub rm"},
	},
	{
		Name:        "account rm",
		Group:       groupAccount,
		Summary:     "Remove an account",
		Usage:       []string{"netivri account rm LOGIN"},
		Description: "Removes the account. It is not idempotent. Takes effect after 'netivri down' and 'netivri up'.",
		Examples:    []helpExample{{"Remove alice", "netivri account rm alice"}},
		Errors:      []helpError{herr("ACCOUNT_NOT_FOUND", cli.ExitNotFound, "no account with that login")},
		SeeAlso:     []string{"account list"},
	},

	{
		Name:    "stats",
		Group:   groupDiag,
		Summary: "Show recorded node health, delay and throughput",
		Usage: []string{
			"netivri stats [--sub NAME] [--country CC] [--node LABEL] [--since DURATION] [--by node|country|provider]",
		},
		Description: "Shows what the daemon recorded about the exit nodes: the health and delay from the background " +
			"probes, the dial time and outcome of real connections, and the throughput of real traffic. It reads " +
			"<home>/stats.db read-only, so it works while the daemon runs and after it stopped. Statistics exist " +
			"only for accounts that have a provider or an exit country.",
		Flags: []helpFlag{
			{"--sub NAME", "Only the nodes of this subscription (provider)."},
			{"--country CC", "Only the nodes of this country."},
			{"--node LABEL", "Only this node. A label is <provider>/<node id>, as in 'netivri node list' (sub and id)."},
			{"--since DURATION", "Window: 90m, 24h, 7d and so on. Default 24h, at most 365d."},
			{"--by GROUP", "node (default), country or provider. Grouped output sums counters and leaves percentiles out."},
		},
		Examples: []helpExample{
			{"Every node over the last day", "netivri stats"},
			{"Compare countries", "netivri stats --by country"},
			{"Compare providers over a week", "netivri stats --by provider --since 7d"},
			{"German nodes of one subscription over the last 90 minutes", "netivri stats --sub main --country DE --since 90m"},
			{"One node", "netivri stats --node main/a1b2c3"},
		},
		Errors: []helpError{
			herr("STATS_UNAVAILABLE", cli.ExitNotFound, "no statistics database yet: nothing was recorded"),
			herr("USAGE", cli.ExitUsage, "invalid --since, --by or --country"),
		},
		SeeAlso: []string{"diag dial", "status"},
	},
	{
		Name:        "diag",
		Summary:     "Measure the real connection path",
		Usage:       []string{"netivri diag dial --account LOGIN | --node LABEL [flags]"},
		Description: "Diagnostics that measure instead of guess. Only 'dial' exists today.",
		Examples:    []helpExample{{"Where does a new connection spend its time?", "netivri diag dial --account alice"}},
		SeeAlso:     []string{"stats"},
	},
	{
		Name:    "diag dial",
		Group:   groupDiag,
		Summary: "Measure where a new connection spends its time",
		Usage:   []string{"netivri diag dial --account LOGIN | --node LABEL [--samples N] [--idle LIST|off] [--max-nodes N] [--url URL]"},
		Description: "Takes a few samples of a new connection through a node and reports, per phase, how long it took " +
			"(dns, tcp, tls for nodes that expose it, and first_byte; p50, p90 and max) and how many attempts failed. " +
			"Then it tests whether a prepared connection survives sitting idle for 0.5 s, 1 s, 2 s and so on, and " +
			"suggests a pool TTL. Use it to see whether DNS, the node or the handshake is the slow part.\n\n" +
			"Give exactly one of --account and --node. It runs in its own process, without the daemon, and sends a " +
			"few small HTTP requests through each node to the probe URL. The output contains node labels only, " +
			"never addresses.",
		Flags: []helpFlag{
			{"--account LOGIN", "Diagnose the route nodes of this account, in route order (at most --max-nodes)."},
			{"--node LABEL", "Diagnose this one node: <provider>/<node id>, as in 'netivri stats'."},
			{"--samples N", "Samples per node, 1 to 20 (default 5)."},
			{"--idle LIST", "Idle times for the survival test: up to 10 durations such as 500ms,2s, ascending; or 'off' to skip the test. Default: a built-in ladder."},
			{"--max-nodes N", "At most this many route nodes of an account, 1 to 50 (default 6)."},
			{"--url URL", "Probe URL, http only. Default: the warm-up URL (www.gstatic.com)."},
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
		Description: "Prints the version of this binary as JSON. A build that is not a release reports a dev version.",
		Examples:    []helpExample{{"Show the version", "netivri version"}},
	},
	{
		Name:    "help",
		Group:   groupOther,
		Summary: "Show this help",
		Usage: []string{
			"netivri help [COMMAND [SUBCOMMAND]]",
			"netivri COMMAND [SUBCOMMAND] --help",
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
