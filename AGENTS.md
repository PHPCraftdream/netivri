# AGENTS.md

Instructions for AI agents that use or develop `netivri`.

## Main rule: build and test only in GitHub CI

All compilation, tests, benchmarks, packaging and install checks run
**only in GitHub Actions CI**. Never run them on a local machine.

Forbidden locally: `go build`, `go test`, `go run`, `go install`,
benchmarks, `npm pack` / `npm install` / `npm test`, running the installer
scripts, any end-to-end run of the binary.

Allowed locally (linters and syntax only):

- `gofmt -l` / `gofmt -w` on our own files (never on `third_party/`)
- `go vet ./...`
- `golangci-lint run` (if installed)
- syntax checks of scripts and configs (`shellcheck`, PowerShell parser,
  YAML/JSON linters)

To verify a change, push it and read the CI result.

## Using netivri

- The output is Ktav by default. Always pass `--json` (or set `NETIVRI_OUTPUT=json`
  once) and parse stdout as one JSON object. Errors arrive on stderr in the same
  format with a `code` field; branch on `code`, not on `message`.
- The full contract is in [`docs/cli.md`](docs/cli.md). `netivri help --json`
  lists the commands and `netivri help {COMMAND} --json` gives the usage,
  flags, examples and error codes of one as a JSON object.
- Typical flow:
  1. `netivri status --json` — exit 4 means the angel is not running.
  2. `netivri up --json` — safe to repeat.
  3. Route by account: `netivri account add {ACCOUNT} --provider {SUBSCRIPTION} --country DE --json`
     (the first client that authenticates as {ACCOUNT} sets its password); a
     running angel applies the change within a few seconds (its listener
     restarts, open connections end) and `netivri status` reports it under
     `reload`. The angel also re-fetches the subscriptions once an hour.
     `--provider` and `--country` also take comma-separated lists in priority
     order (`--provider main,backup --country DE,NL`).
     `netivri use`, `netivri node test` and `netivri schema` are described in
     `docs/cli.md` but not implemented yet.
  4. Use the proxy at `data.listen` from `status`.
- `netivri doctor --json` checks the whole setup in one pass (exit 7 when a
  check failed); `netivri logs --since 10m --json` reads the angel log.
- `netivri status` is short; pass `--full` for `warm.nodes`, `stats` and
  `affinity`. `netivri help config`, `netivri help routing` and
  `netivri help output` (also with `--json`) explain the configuration file,
  routing and the fields of every command without the repository.
- Never print or log subscription URLs; they are credentials. SOCKS5 chain
  URLs (`pre_socks5`, `post_socks5`) are credentials too.
- SOCKS5 around the VPN node: `chain.pre_socks5` / `chain.post_socks5` in
  `config.ktav`, per account `netivri account set-socks5 {ACCOUNT} --pre
  {URL|none|inherit} --post {URL|none|inherit}` (also `account add
  --pre-socks5/--post-socks5`); an account value overrides the global one,
  `none` cancels it. `account show` and `status` show the redacted effective
  values.
- Do not bind the proxy to anything other than `127.0.0.1` unless the user
  asked for it.

## Developing netivri

- Go, single module, single binary `cmd/netivri`.
- Mihomo is used as a library; do not shell out to a mihomo binary. Its
  sources are vendored in `third_party/mihomo` (see `third_party/README.md`):
  change them only through documented patch commits.
- Every user-facing behavior change must update `docs/cli.md` in the same change.
- In the help of the program and in every document, a placeholder is written in
  curly braces with a name that says what goes there: `{ACCOUNT}` (the login of
  an account), `{SUBSCRIPTION}`, `{COUNTRY}`, `{NODE}`, `{URL}`, `{DIR}`. Not
  `<name>`, not a bare upper-case word. A test guards the help.
- Changing the JSON shape or exit/error codes is breaking: bump `schema_version`.
- Builds and tests run only in CI — see the main rule at the top.
- No TUN, no system proxy changes in v1.

## Testing

See [`docs/testing.md`](docs/testing.md) for the full strategy. Hard rules:

- Every change ships tests in the right scope, in the same change.
- Scopes are **package** (AREA: `go test ./internal/foo/...`) plus **level**
  (build tags: unit = no tag, `integration`, `e2e`, `bench`).
- Tests run only in GitHub CI — never locally (see the main rule above).
