# Testing strategy

All tests run **only in GitHub Actions CI** (see the main rule in
`AGENTS.md`). Locally only `gofmt`, `go vet` and syntax checks are allowed.

## Scopes by AREA (package)

A Go package is a scope. Any subset of packages can be tested standalone:

```
go test ./internal/config/...
```

In CI the package scope is selected with the `packages` workflow-dispatch
input, or locally via `CI_PACKAGES` (see below). Default: `./...`.

## Scopes by LEVEL (build tags)

Each test file belongs to exactly one level, expressed as a build tag.
The level determines what the test is allowed to touch:

| Level          | Tag            | File name                   | Allowed                                       |
|----------------|----------------|-----------------------------|-----------------------------------------------|
| unit (default) | none           | `*_test.go`                 | Hermetic: no sockets, no network, no external processes. May load the Ktav native library via `KTAV_LIB_PATH`. |
| integration    | `integration`  | `*_integration_test.go`     | Loopback sockets (`127.0.0.1:0`), embedded Mihomo core, `t.TempDir()` files/dirs. |
| e2e            | `e2e`          | `*_e2e_test.go`             | Builds and spawns the real `netivri` binary.   |
| bench          | `bench`        | `*_bench_test.go`           | Benchmarks (hermetic).                         |

Every tagged file must start with the matching `//go:build` line
(`//go:build integration`, `//go:build e2e`, `//go:build bench`).
Unit test files have **no** build tag.

## Test rules

- **Deterministic.** Never sleep for synchronization. Use channels,
  `context` deadlines and polling with short deadlines.
- **Ports** come from the kernel: listen on `127.0.0.1:0` (or use
  `testutil.FreeLoopbackAddr`, which lives in package `testutil` behind
  the `integration` build tag (`testutil_integration.go`), so it is
  available to integration tests only) — never hardcode ports.
- **Files** go into `t.TempDir()`; **environment** is set with `t.Setenv`
  (or `testutil.TempHome` for `NETIVRI_HOME`).
- **Table-driven** tests where the cases are natural.
- **No real internet.** Anything network-facing is tested against a local
  fake HTTP/SOCKS server on loopback.
- **No secrets in test output.** Never print subscription URLs or tokens;
  fake credentials only.
- **Ktav native library.** A test that needs it must **fail clearly**
  (never skip) when `KTAV_LIB_PATH` is unset or points to a missing file.
  Call `testutil.RequireKtavLib(t)` at the start of such a test; CI always
  sets `KTAV_LIB_PATH` via `scripts/fetch-ktav-lib.sh`, so a failure means
  a broken workflow, not an optional feature.

## Picking a scope in CI

Manual runs use `workflow_dispatch` inputs:

- `packages` — Go package pattern(s) (default `./...`).
- `tags` — extra comma-separated build tags (default empty).
- `run` — `-run` regex to select individual tests (default: all).
- `os` — `all`, `ubuntu`, `windows` or `macos` (default `all`).
- `race` — enable the race detector (default true).

Push and pull_request events use the defaults (`./...`, all levels, all OS).

Each CI job selects its own tags:

- **unit** — always runs untagged; the user `tags` input is ignored
  there, keeping the test hermetic and the coverage floors unit-only.
- **integration** — always adds the `integration` tag plus the user tags.
- **e2e** — adds the `e2e` tag plus the user tags.
- **bench** — runs with the user tags (the job only triggers when `tags`
  contains `bench`).

Locally equivalent selection is done through env vars understood by
`scripts/ci-test.sh` (not executed locally, only in CI):

```
CI_PACKAGES=./internal/config/... CI_TAGS=integration CI_RUN=TestParse CI_RACE=1 CI_COVER=1 bash scripts/ci-test.sh
```

## Coverage policy

- Every package has a coverage floor in `scripts/coverage-min.txt`
  (`<package-import-path-suffix> <min-percent>`, or `default <pct>`).
- `scripts/check-coverage.sh` parses the Go coverage profile and fails when
  any package is below its floor.
- New code ships with tests in the right scope **in the same change**;
  lowering a floor requires justification in the commit/PR description.
- Floors are only enforced on full runs (`packages == './...'` and no
  `-run` filter); partial runs upload coverage but do not fail floors.
