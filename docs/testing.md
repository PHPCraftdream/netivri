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
- `benchtime` — `go test -benchtime` for the bench job, e.g. `1x`,
  `2s`, `5x` (default `1x`).

Push and pull_request events use the defaults (`./...`, all levels, all OS).

Each CI job selects its own tags:

- **unit** — always runs untagged; the user `tags` input is ignored
  there, keeping the test hermetic and the coverage floors unit-only.
- **integration** — always adds the `integration` tag plus the user tags.
- **e2e** — adds the `e2e` tag plus the user tags.
- **bench** — runs with the user tags (the job only triggers when `tags`
  contains `bench`). The job runs `go test -bench . -benchmem -benchtime
  <benchtime> -run '^$'` and appends every `Benchmark...` line as a fenced
  code block to the job summary (`-benchmem` allocations/bytes included).
  Benchmarks (all hermetic, loopback port 0 only):

  - `BenchmarkConnectDirect` — one TCP connection, SOCKS5 no-auth handshake,
    CONNECT to a loopback echo backend, 1-byte round trip, close.
  - `BenchmarkConnectRouted` — the same iteration through the sticky-group
    path (authenticator, route of two fake SOCKS5 upstreams), serial.
  - `BenchmarkConnectRoutedParallel` — the same, `b.RunParallel`.
  - `BenchmarkThroughputRouted` — 32 MiB each way over one routed
    connection in 64 KiB writes (`b.SetBytes`, MB/s).
  - `BenchmarkParallelConnects1000` — 1000 concurrent routed
    connect+echo+close per iteration, released together.
  - `BenchmarkStickyChooseHit` / `BenchmarkStickyChooseHitParallel` — the
    pre-bound hit path of the sticky group.
  - `BenchmarkStickyChooseMiss` — selection on a new key, 2 and 20 members.
  - `BenchmarkSiteKey` — the site-key reduction for a subdomain, a
    multi-label suffix and an IP literal.
  - `BenchmarkHealthRecordDial` — the health table dial recording.
  - `BenchmarkRecord` / `BenchmarkRecordParallel` — enqueueing samples
    into an open stats store.
  - `BenchmarkNodesQuery` — `Nodes` over ~100000 pre-recorded samples of
    50 nodes.
  - `BenchmarkFlushBatch` — 256 samples recorded and flushed per iteration.

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
