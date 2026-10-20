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

- Always pass `--json` (or rely on non-TTY detection) and parse stdout as one
  JSON object. Errors arrive on stderr as JSON with a `code` field; branch on
  `code`, not on `message`.
- The full contract is in [`docs/cli.md`](docs/cli.md); `netivri schema` prints
  it as JSON.
- Typical flow:
  1. `netivri status --json` — exit 4 means the daemon is not running.
  2. `netivri up --json` — safe to repeat.
  3. `netivri use --country DE --fastest --json`.
  4. Use the proxy at `data.listen` from `status`.
- Never print or log subscription URLs; they are credentials.
- Do not bind the proxy to anything other than `127.0.0.1` unless the user
  asked for it.

## Developing netivri

- Go, single module, single binary `cmd/netivri`.
- Mihomo is used as a library; do not shell out to a mihomo binary. Its
  sources are vendored in `third_party/mihomo` (see `third_party/README.md`):
  change them only through documented patch commits.
- Every user-facing behavior change must update `docs/cli.md` in the same change.
- Changing the JSON shape or exit/error codes is breaking: bump `schema_version`.
- Builds and tests run only in CI — see the main rule at the top.
- No TUN, no system proxy changes in v1.

## Testing

See [`docs/testing.md`](docs/testing.md) for the full strategy. Hard rules:

- Every change ships tests in the right scope, in the same change.
- Scopes are **package** (AREA: `go test ./internal/foo/...`) plus **level**
  (build tags: unit = no tag, `integration`, `e2e`, `bench`).
- Tests run only in GitHub CI — never locally (see the main rule above).
