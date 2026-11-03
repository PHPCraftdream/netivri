# Interactive terminal shell

`internal/tui` provides a reusable Bubble Tea v2 shell. The CLI hosts implemented Providers, Accounts, and Servers screens, selecting the initial screen from `-i` entry routing. The frame owns tabs, header status, help, toast, modal state, and background activity. Screens are lazy-created, retained across tab changes, and initialized once. Background results return to their requesting screen. Header polls run serially every two seconds; stopped and unavailable states do not prevent navigation.

## Screen operations and asynchronous work

Providers uses `ProviderList`, `ProviderAdd`, `ProviderRefresh`, `ProviderRemove`, and `ProviderCountries` application operations. The initial provider list is loaded synchronously; subsequent reloads, fetches, mutations, and other screen reads run as Bubble Tea background commands and results are routed to the requesting screen. Accounts uses `AccountList`, `AccountShow`, `AccountAdd`, `AccountUpdate`, `AccountClone`, and `AccountRemove`; account activity/rate status is polled in the background about once per second. Server inspection uses `ServerList` and `ServerShow` and is read-only. Persistence remains through existing application operations; a running angel observes saved edits automatically.

## Frame and screen keys

`netivri -i` starts at Accounts; `sub`, `account`, and `server` start at Providers, Accounts, and Servers. Tabs are Providers, Accounts, Servers. Tab/Right and Shift-Tab/Left navigate; 1/2/3 select; `?` opens help (Esc or `?` closes); `q` and Ctrl-C quit. When a form/detail captures input, global navigation/help/quit keys do not apply; Ctrl-C always quits.

All tables use Up/Down or j/k, PgUp/PgDn, Home/End. Providers: `r` reload, `a` add, `u` refresh selected, `U` refresh all, `d` remove, Enter countries. Countries reload with `r` and return with Esc; refresh-result views scroll and close with Esc. Add fields are Name, URL, Via. Accounts: Enter details, `a` add, `e` edit, `c` clone, `d` delete, `r` reload; detail returns with Esc or Backspace. Servers: Enter details, `r` reload; detail returns with Esc or `q`. Server details display parameter keys only; values, including secrets, are not rendered.

Forms use Tab or Enter to advance; at the final field either submits. Shift-Tab goes back, and Esc cancels. Up/Down changes choice selection and Space toggles multi-choice selections. Confirmations use `y` for yes, `n` or Enter for no, Esc to cancel.

## Forms and secrets

Provider add fields are Name, URL, and Via. URL and Via are masked, and secret-containing errors are redacted. Account add fields cover login, providers, country, exit route, allowed clients, and pre/post hop URLs; edit covers providers, country, exit, allowed clients, and hop URLs. Empty edit hop fields mean unchanged; `none` clears a hop. Clone accepts a new login and optional overrides. Hop fields are masked and values are redacted from errors. Account screens show password state only; passwords are never displayed or set by the shell. The first proxy password used for an account establishes its password. Form values are short-lived and must never be logged.

## Terminal and CI behavior

The CLI requires terminal stdin/stdout and excludes JSON mode; otherwise it reports `NOT_A_TERMINAL` (exit 2) or `USAGE`. `--home {DIR}` and `--local` use regular state resolution. Bubble Tea, Bubbles, and Lipgloss are MIT-licensed. Unit tests exercise the frame/screens directly; `cmd/netivri/tui_e2e_test.go` verifies real-binary JSON exclusion, piped-terminal error, and unsupported status route using temporary homes and no ports. Tests/builds run only in CI (`docs/testing.md`).
