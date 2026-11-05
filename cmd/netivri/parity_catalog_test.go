package main

// The capability catalog of docs/design/parity.md. A CLI ref is a help topic
// name ("sub add") or a topic plus a documented flag ("sub add --name"). A TUI
// ref is "screen/id" as reported by ParityRefs, or "frame/key".
//
// Edit this file whenever a command, a flag, a screen or a card line is added
// or removed; parity_test.go fails until the catalog agrees with both sides.

type exemption struct {
	Side   string // "cli" or "tui"
	Reason string
	// Gap is the id of the task that builds the missing side; empty for a
	// permanent exemption.
	Gap string
}

type capability struct {
	ID     string
	CLI    []string
	TUI    []string
	Exempt []exemption
}

func permanent(side, reason string) exemption { return exemption{Side: side, Reason: reason} }
func gap(side, task, reason string) exemption {
	return exemption{Side: side, Reason: reason, Gap: task}
}

// parityGapBudget is the number of exemptions that carry a gap id. It only
// goes down: closing a gap lowers it in the same change, adding one is a
// failure. The parity work is finished when it is zero.
const parityGapBudget = 4

var parityCatalog = []capability{
	// Providers
	{ID: "provider.list", CLI: []string{"sub list"}, TUI: []string{"providers/list"}},
	{ID: "provider.add", CLI: []string{"sub add", "sub add --name", "sub add --via"}, TUI: []string{"providers/add"}},
	{ID: "provider.preview", CLI: []string{"sub countries --url", "sub countries --via"},
		Exempt: []exemption{gap("tui", "207", "preview a subscription's countries before adding it")}},
	{ID: "provider.countries", CLI: []string{"sub countries"}, TUI: []string{"providers/countries"}},
	{ID: "provider.refresh", CLI: []string{"sub update", "sub update --all"}, TUI: []string{"providers/refresh", "providers/refresh-all"}},
	{ID: "provider.remove", CLI: []string{"sub rm"}, TUI: []string{"providers/remove"}},
	{ID: "provider.hops", CLI: []string{"sub add --pre-socks5", "sub add --post-socks5", "sub set-socks5", "sub set-socks5 --pre", "sub set-socks5 --post"},
		TUI: []string{"providers/pre", "providers/post", "providers/clear-pre", "providers/clear-post"}},
	{ID: "provider.rename", CLI: []string{"sub rename"}, TUI: []string{"providers/edit-name"}},
	{ID: "provider.url", CLI: []string{"sub set", "sub set --url"}, TUI: []string{"providers/subscription", "providers/edit-subscription"}},
	{ID: "provider.via", CLI: []string{"sub set --via"}, TUI: []string{"providers/via", "providers/edit-via"}},
	{ID: "nodes.list", CLI: []string{"node list", "node list --sub", "node list --country"}, TUI: []string{"providers/nodes"}},

	// Accounts
	{ID: "account.list", CLI: []string{"account list"}, TUI: []string{"accounts/list"}},
	{ID: "account.show", CLI: []string{"account show"},
		Exempt: []exemption{gap("tui", "207", "route tiers with live node state")}},
	{ID: "account.add", CLI: []string{"account add", "account add --exit", "account add --provider", "account add --country", "account add --clients", "account add --pre-socks5", "account add --post-socks5"},
		TUI: []string{"accounts/add"}},
	{ID: "account.clone", CLI: []string{"account clone", "account clone --exit", "account clone --password", "account clone --provider", "account clone --country", "account clone --clients"},
		TUI: []string{"accounts/clone"}},
	{ID: "account.exit", CLI: []string{"account set-exit"}, TUI: []string{"accounts/exit"}},
	{ID: "account.country", CLI: []string{"account set-country", "account set-country --none"}, TUI: []string{"accounts/countries"}},
	{ID: "account.provider", CLI: []string{"account set-provider", "account set-provider --none"}, TUI: []string{"accounts/provider"}},
	{ID: "account.clients", CLI: []string{"account set-clients", "account set-clients --none"}, TUI: []string{"accounts/clients"}},
	{ID: "account.hops", CLI: []string{"account set-socks5", "account set-socks5 --pre", "account set-socks5 --post"},
		TUI: []string{"accounts/pre", "accounts/post", "accounts/clear-pre", "accounts/clear-post"}},
	{ID: "account.bulk", CLI: []string{"account set-country --all", "account set-provider --all", "account set-clients --all", "account set-exit --all"},
		Exempt: []exemption{gap("tui", "210", "apply a field to several or all accounts")}},
	{ID: "account.remove", CLI: []string{"account rm"}, TUI: []string{"accounts/remove"}},

	// Servers
	{ID: "server.list", CLI: []string{"server list", "server show"}, TUI: []string{"servers/list"}},
	{ID: "server.add", CLI: []string{"server protocols", "server add", "server add --protocol", "server add --account", "server add --port", "server add --name", "server add --bind", "server add --public-host", "server add --default", "server add --param"},
		TUI: []string{"servers/add"}},
	{ID: "server.link", CLI: []string{"server link", "server link --account", "server link --host", "server link --format"}, TUI: []string{"servers/link"}},
	{ID: "server.link-qr", CLI: []string{"server link --qr"},
		Exempt: []exemption{permanent("tui", "the CLI flag is declared unsupported (no QR encoder is included), so there is nothing to mirror")}},
	{ID: "server.rotate", CLI: []string{"server rotate", "server rotate --account"}, TUI: []string{"servers/rotate"}},
	{ID: "server.remove", CLI: []string{"server remove"}, TUI: []string{"servers/remove"}},
	{ID: "server.update", CLI: []string{"server update", "server update --public-host", "server update --bind", "server update --port", "server update --account", "server update --param"},
		TUI: []string{"servers/bind", "servers/port", "servers/publichost", "servers/accounts", "servers/param"}},
	{ID: "server.reload", TUI: []string{"servers/reload"},
		Exempt: []exemption{permanent("cli", "every CLI run reads the current state")}},

	// Service and diagnostics
	{ID: "service.up", CLI: []string{"up"}, TUI: []string{"service/start"}},
	{ID: "service.up-flags", CLI: []string{"up --port", "up --bind"},
		TUI: []string{"settings/listen.port", "settings/listen.bind"}},
	{ID: "service.restart", TUI: []string{"service/restart"},
		Exempt: []exemption{permanent("cli", "a restart is down followed by up; the CLI keeps them separate")}},
	{ID: "service.foreground", CLI: []string{"up --foreground"},
		Exempt: []exemption{permanent("tui", "running in the foreground ties the angel to a terminal; the shell is itself a foreground program")}},
	{ID: "service.down", CLI: []string{"down"}, TUI: []string{"service/stop"}},
	{ID: "service.status", CLI: []string{"status", "status --full", "status --watch", "status --interval"}, TUI: []string{"service/list"}},
	{ID: "service.doctor", CLI: []string{"doctor"}, TUI: []string{"service/checks"}},
	{ID: "service.logs", CLI: []string{"logs", "logs --follow", "logs --level", "logs --since", "logs --tail"},
		Exempt: []exemption{gap("tui", "207", "angel log view")}},
	{ID: "service.stats", CLI: []string{"stats", "stats --sub", "stats --country", "stats --node", "stats --since", "stats --by", "stats --account"},
		TUI: []string{"stats/list", "stats/by", "stats/since", "stats/sub", "stats/country", "stats/node", "stats/account", "stats/reload"}},
	{ID: "service.diag", CLI: []string{"diag dial", "diag dial --account", "diag dial --node", "diag dial --samples", "diag dial --idle", "diag dial --max-nodes", "diag dial --url"},
		TUI: []string{"providers/diagnose"}},
	{ID: "service.version", CLI: []string{"version"},
		Exempt: []exemption{permanent("tui", "the shell header shows the version")}},

	// Global settings: file only today
	{ID: "settings.global", CLI: []string{"config get", "config set"},
		TUI: []string{"settings/list", "settings/listen.allowed_clients", "settings/auth.login", "settings/auth.password", "settings/auth.allow_init_remote", "settings/fetch.proxy", "settings/fetch.update_interval", "settings/chain.pre_socks5", "settings/chain.post_socks5"}},

	// The shell's own navigation
	{ID: "frame.tabs", TUI: []string{"frame/tabs"}, Exempt: []exemption{permanent("cli", "navigation of the shell")}},
	{ID: "frame.help", TUI: []string{"frame/help"}, Exempt: []exemption{permanent("cli", "the CLI has `netivri help`, which the shell mirrors with `?`")}},
	{ID: "frame.quit", TUI: []string{"frame/quit"}, Exempt: []exemption{permanent("cli", "navigation of the shell")}},
}

// parityFrameRefs are the keys owned by the frame; they are not widgets of a
// screen, so the list is kept here and checked only for catalog agreement.
var parityFrameRefs = []string{"frame/tabs", "frame/help", "frame/quit"}
