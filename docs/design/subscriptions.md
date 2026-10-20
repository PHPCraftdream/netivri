# Subscriptions design (internal/subscription)

Scope: parsing, identification and on-disk storage of provider
subscriptions. Fetching lives in `internal/fetch`; this package only
receives the downloaded bytes. Configuration of subscription entries
(`Subscription{Name,URL,Via}`) lives in `internal/config`.

## Formats and detection

`Parse(content []byte) ([]Node, error)` / `ParseDetailed` auto-detect the
format:

1. **Clash/Mihomo YAML** — a document whose `proxies:` key holds a list of
   proxy mappings (checked first).
2. **URI list** — plain text or base64 of `ss://`, `vmess://`, `vless://`,
   `trojan://`, `hysteria://`, `hysteria2://`, `tuic://` lines, decoded
   through Mihomo's `common/convert.ConvertsV2Ray` (base64 handled
   transparently).

Anything else (empty body, HTML, garbage) yields a `SUBSCRIPTION_EMPTY`
error (`*cli.Error`, exit `cli.ExitNetwork`). A body larger than **16 MiB**
fails with `SUBSCRIPTION_TOO_LARGE`; more than **5000** entries fail with
`SUBSCRIPTION_TOO_MANY`.

## Validation and skipping

Every candidate mapping is validated with Mihomo's own
`adapter.ParseProxy`, so netivri never invents proxy semantics. Entries are
dropped and counted per reason in `ParseResult.Skipped`:

- `banner` — provider pseudo-nodes: names containing traffic/expiry banner
  terms ("剩余流量", "Expire", "traffic", "到期", "官网", "t.me", ...),
  loopback servers (127.0.0.1, 0.0.0.0, ::1, localhost) or port <= 0.
- `invalid` — failed `adapter.ParseProxy`, or no usable name.

Lines the URI converter silently skips (no scheme) are not counted.
Nodes with identical IDs are deduplicated, keeping the first occurrence;
provider order is otherwise preserved. When no valid node remains the
error is `SUBSCRIPTION_EMPTY`.

## Node identity

`Node.ID` is the first 12 hex chars of `sha256(type|server|port|name)`.
It is stable across updates as long as those fields are unchanged and
sensitive to each of them.

## Country inference

`CountryFromName` is table-driven and offline (no GeoIP, no network):

1. flag emoji (two regional indicator symbols; the England/Scotland/Wales
   black-flag tag sequences map to `GB`);
2. an explicit ISO alpha-2 token delimited by non-letters at the start or
   in brackets/pipes ("[US]", "US-1", "|DE|", "HK 03"), validated against
   the full ISO list. Ambiguous bare 2-letter tokens (also English words:
   "NO", "IN", "IS", "AT", "BE", "US", ...) are only trusted when the
   whole name is the token, or when bracketed/punctuation-delimited —
   never as substrings or as bare words inside longer names;
3. English country names, aliases and major exit cities (whole words,
   unigrams and bigrams: "United States", "Frankfurt", "Tokyo");
4. common Russian names (Россия, Германия, США, Нидерланды, ...);
5. common Chinese names (美国, 日本, 香港, 台湾, 新加坡, ...) as
   substrings (CJK has no word boundaries).

Best effort: returns `""` when nothing matches; `node list` reports the
source as `name` (see docs/cli.md) and `use --country` may therefore miss
nodes whose names carry no signal.

## Countries

The exit country of a node is inferred from its provider-supplied name by
`CountryFromName` (see "Country inference"); it is best effort and stored
with the node. `CountryCounts` summarises a node list into
`[]CountryCount{code, nodes}` sorted by code plus a count of nodes without
a country.

`netivri sub countries` reports these counts: for a NAME from the stored
snapshot (no network), for every configured provider plus a summed union
with no argument, and for `--url URL [--via PROXY]` by fetching through
the same strict path as `sub add` (the `--via` proxy or `fetch.proxy`, no
direct fallback) without storing anything. A provider without a stored
snapshot is reported with null fields; the URL is never printed.

## On-disk layout

`Store` persists one JSON file per subscription under
`<home>/subscriptions/`:

- `<name>.json` — `Snapshot{fetched_at, nodes[], skipped}` including each
  node's **raw proxy mapping** (needed to build the outbound later).
- Names must match `^[A-Za-z0-9._-]{1,64}$` and are not `.`/`..`
  (path traversal); every Store method validates.
- Files are **secret**: mode `0600`, directory `0700`, written atomically
  (temp file in the same directory, fsync, rename; transient Windows
  rename errors 5/32/33 are retried, mirroring internal/config).
- `Load` of a missing subscription wraps `os.ErrNotExist`; a corrupt file
  is an error, never a panic.

### What is secret

Raw proxy mappings contain credentials (passwords, UUIDs); the stored
files are the only place they are serialised. `Node.Raw` is tagged
`json:"-"`, so CLI listings and logs never include them; the on-disk
format uses an internal storage type that carries `raw` explicitly.
Subscription URLs are credentials and are never printed here (redaction
is the caller's job, see docs/config.md).

## How the fetcher and the CLI use this package

- `sub add/update`: fetch (internal/fetch) → `Parse` → `Store.Save` with
  `FetchedAt` and the total skip count.
- `node list`: reads snapshots via `Store.Load`/`Store.List`; filterable
  by `--sub` and `--country`.
- `use --country CC`: selects among nodes whose `Country` matches.

## Behavior notes and limits

- **Logging.** Mihomo's share-link converter prints the offending raw line
  (credentials included) through its global logger, which writes to stdout.
  `Parse`/`ParseDetailed` silence that logger once per process.
- **Duplicate names in URI lists.** The converter makes repeated names
  unique by appending `-NN`; provider names are therefore kept only for the
  first occurrence, and the id of a later duplicate depends on list order.
  YAML names are never rewritten.
- **Numbers on disk.** `Raw` is stored as JSON, so integers read back as
  `float64`; Mihomo's decoder accepts that for integer fields.
- **Rejected input.** When every entry is rejected, `ParseDetailed` returns
  the skip counts together with `SUBSCRIPTION_EMPTY`; `Parse` returns only
  the error.
- **Writers.** In-process writers are serialised by one package-level mutex,
  not per `Store`, because several stores may share a directory.

## Non-goals

No GeoIP database, no health checks/latency testing, no subscription URL
management (that is internal/config + internal/fetch), no background
refresh scheduling.
