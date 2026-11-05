# CLI / interactive shell parity

Rule: **everything the CLI can do, the shell (`netivri -i`) can do, and the
other way round.** Not a convention but a test: a catalog of capabilities that
both surfaces are checked against, so a new command, flag, card line or action
on one side that the other side lacks (and that is not an explicit, reasoned
exemption) fails CI.

## Why a catalog

The CLI surface is the help topics (`cmd/netivri/help_topics.go`: commands and
their documented flags). The shell surface is the screens (tab list, `+ Add`
rows, card field and action lines, frame-level keys). Neither can be derived
from the other, so a third thing names what the user can *do*:

```go
// cmd/netivri/parity_catalog_test.go (test-only data, nothing is shipped)
type capability struct {
    ID     string   // "provider.rename", "account.set-exit", "service.up"
    CLI    []string // refs: "sub set-socks5", "sub set-socks5 --pre", ...
    TUI    []string // refs: "providers/edit-name", "providers/add", "frame/help", ...
    Exempt []exemption // a side that has no ref, with the reason
}
type exemption struct {
    Side   string // "cli" or "tui"
    Reason string // why this side does not offer it (never "not done yet")
    Gap    string // task id when the reason is "not built yet"; empty for a permanent exemption
}
```

A **CLI ref** is a help topic name (`sub add`) or a topic name plus one of its
documented flags (`sub add --pre-socks5`). A **TUI ref** is `{screen}/{id}`:
`providers`, `accounts`, `servers`, `service`, `logs`, `stats`, ... with the
card line or action id (`edit-name`, `clear-pre`), `add` for the `+ Add` row,
or `frame/{key}` for keys owned by the frame (tabs, help, quit).

## Checks (`cmd/netivri/parity_test.go`; the shell side is read from the live screens through `ParityRefs`, `parity_refs.go`)

1. **CLI listed.** Every leaf help topic and every flag documented on it is
   referenced by a capability, or is exempt with a reason.
2. **TUI listed.** The screens are built from a small fixture (one item each,
   so item cards exist). Every `+ Add` row, every card line that is a field or
   an action, and every frame key is referenced by a capability.
3. **Refs exist.** Every ref in the catalog exists on its side (a renamed
   command or card line breaks the catalog, not silently the coverage).
4. **Both sides or a reason.** A capability with no CLI ref or no TUI ref has
   an exemption for that side with a reason. Exemptions are not stale: an
   exempted side must really have no ref.
5. **Gaps shrink.** An exemption with `Gap` set names a task; the list of such
   exemptions is printed by the test and every one must be closed by the end of
   the parity work (a final test asserts there are none left).
6. **Same effect** (dual run, mutating capabilities only). For each, a scenario
   runs the CLI command and the shell walk (real keys) on identical fixture
   homes and compares the resulting `config.ktav` and the stored snapshots
   (secrets compared by hash, never printed). This is what makes "cover each
   other" mean the same behavior, not just a button with the same name.
   Implemented in `cmd/netivri/parity_dual_*_test.go`: scenarios register under
   the capability id in `dualScenarios`; a capability with both sides that has
   no scenario must be listed in `dualNoScenario` with a reason (read-only, or
   a process effect), so a new mutating capability cannot skip the check.

Permanent exemptions (kept short and reasoned): `version` (the shell header
shows the version), `help` (the shell has `?`), `-i` itself, JSON/`--quiet`
output switches (they shape CLI output; the shell has no such mode), the
internal foreground flag of `up`.

## Inventory and gaps (state when this was written)

Only in the CLI, to be built into the shell:

| Capability | CLI | Shell today |
| --- | --- | --- |
| Start / stop the angel, port and bind options | `up`, `down` | none (the header shows the state) |
| Full status, accounts, activity | `status [--full\|--watch]` | header only |
| Health check | `doctor` | none |
| Logs, follow | `logs [--follow]` | none |
| Node statistics | `stats` | none |
| Dial diagnostics | `diag dial` | none |
| Stored nodes | `node list` | none |
| Account route tiers and live state | `account show` | card shows fields, not tiers |
| Preview a subscription's countries before adding | `sub countries --url` | none |
| Re-fetch one or all | `sub update` | `Refresh` / `Refresh all` (covered) |

Only in the shell, to be built into the CLI:

| Capability | Shell | CLI today |
| --- | --- | --- |
| Rename a provider (accounts follow) | `Rename` | none (`ProviderRename` is shell-only) |
| Change a provider's URL or `via` proxy | `Edit subscription`, `Edit Via` | none (`ProviderUpdate` is shell-only) |
| Change a server's fields | field lines on the Servers card | none (`ServerUpdate` is shell-only) |

In neither (global settings live in `config.ktav` only: `listen.*`, `auth.*`,
`fetch.*`, `chain.*`): a `config get|set` command and a Settings screen.

## Design of the missing shell screens

* **Service** tab (after Servers, so the entry points keep their indexes): state, listen address, uptime, version; actions
  `Start`, `Stop`, `Restart` (confirm names the open connections that will
  drop), `Doctor` (runs the checks and lists them with the hint of every
  failure). Same operations as `up` / `down` / `doctor` (functions shared with
  the CLI in `ops_service.go`), nothing shell-only.
* **Status** pane on the Service tab: the accounts and the activity of
  `status --full`, refreshed on a tick; the `--watch` view.
* **Logs** tab: the tail of the angel log, follow on by default (`f` toggles),
  level filter, `/` search; read-only.
* **Stats** tab: per node or per account (a choice), the table of `stats`.
* **Nodes**: a read-only list on the Providers card (`Nodes` line opens it) =
  `node list`; `Diagnose...` action runs `diag dial` for the selected node and
  shows the percentiles.
* **Account card**: `Route...` shows the tiers with live node state
  (= `account show`).
* **Provider add wizard**: a `Preview countries` step before the fetch (=
  `sub countries --url`).
* **Settings** tab: the global keys as fields (listen, auth, fetch, chain), the
  same validation as `config set`.

Every screen follows `tui-ux.md` (frame regions, list+card, prompts, no secret
in any view) and gets the goldens at 60x16, 80x24, 100x30, 120x40.

## Design of the missing CLI commands

* `netivri sub rename {OLD} {NEW}` -> `ProviderRename`.
* `netivri sub set {NAME} [--url {URL}] [--via {PROXY|none}]` ->
  `ProviderUpdate` (the URL is a credential: never echoed).
* `netivri server update {NAME} [--field value]...` -> `ServerUpdate`, the
  fields are the protocol's parameter schema (`server protocols`).
* `netivri config get [{KEY}]` / `config set {KEY} {VALUE}` for the global
  keys (validated like the file; secrets never printed, `config get` shows
  `set` / `not set`).

Each with help topic, examples, `docs/cli.md`, CHANGELOG and tests.

## Order of work

1. This design (done) and the catalog with checks 1-5, initial exemptions all
   carrying a gap id, so CI is green and the gaps are the visible backlog.
2. Missing CLI commands (`sub rename`, `sub set`, `server update`).
3. Service, Logs, Stats tabs and the Nodes / Diagnose / Route / Preview pieces.
4. `config get|set` and the Settings tab.
5. Check 6 (dual run) for every mutating capability, then the final "no gaps"
   assertion (done: the gap budget is 0 and stays 0).
