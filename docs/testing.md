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
| unit (default) | none           | `*_test.go`                 | Hermetic: no sockets, no network, no external processes. |
| integration    | `integration`  | `*_integration_test.go`     | Loopback sockets (`127.0.0.1:0`), embedded Mihomo core, `t.TempDir()` files/dirs. |
| e2e            | `e2e`          | `*_e2e_test.go`             | Builds and spawns the real `netivri` binary.   |
| bench          | `bench`        | `*_bench_test.go`           | Benchmarks (hermetic).                         |

Every tagged file must start with the matching `//go:build` line
(`//go:build integration`, `//go:build e2e`, `//go:build bench`).
Unit test files have **no** build tag.

## Parity of the CLI and the shell

`cmd/netivri/parity_test.go` checks the capability catalog
(`parity_catalog_test.go`) against the help topics and flags on one side and
the live screens (`ParityRefs`) on the other, and keeps the list of known gaps
as a budget that only goes down. See [`design/parity.md`](design/parity.md).

## Test rules

- **Deterministic.** Never sleep for synchronization. Use channels,
  `context` deadlines and polling with short deadlines.
- **Budgets.** A whole-test `context` is a generous ceiling (minutes: a
  Windows runner can take several times longer than a laptop), not a tuned
  limit; bound each wait with its own short child deadline. A tight
  whole-test budget makes the last wait of a long test fail with a message
  about a different cause (the reload that "never happened" was only the
  expired context).
- **Ports** come from the kernel: listen on `127.0.0.1:0` (or use
  `testutil.FreeLoopbackAddr`, which lives in package `testutil` behind
  the `integration` build tag (`testutil_integration.go`), so it is
  available to integration tests only) — never hardcode ports.
- **Files** go into `t.TempDir()`; **environment** is set with `t.Setenv`
  (or `testutil.TempHome` for `NETIVRI_HOME`).
- **Clock ticks.** The Windows clock advances about every 15 ms. A test that
  needs "B strictly after A" (a probe after a connection began, a timestamp
  newer than another) must put real time, or an injected clock step, between
  the two events; two events a few milliseconds apart can tie there and pass
  on Linux and macOS only.
- **Terminal screens** (`netivri -i`, `internal/tui`) are tested without a
  terminal: drive the screen's `Update` and `View` (or `teatest`) and assert
  on the rendered text with ANSI stripped and on the config read back from a
  temporary home. Send keys as real key messages (`tea.KeyPressMsg{Code:
  tea.KeySpace}`; a message with only `Text: " "` is not a space press).
- **Failure-only diagnostics.** Where a test can fail for a reason its
  assertion does not show (a process that vanished, a flaky end-to-end flow),
  register the detail with `testutil.LogOnFail(t, title, report)`: it is
  logged only if the test failed and costs nothing on a green run. Register
  it after the cleanup that stops what the report reads (cleanups run last
  in, first out). Every end-to-end `startUp` already does this: a failed test
  prints how the angel process ended, the tail of its log, the crash dumps
  of its home and its `status --full`, redacted like all test output. A
  failure helper must name the primary error before any check that can stop
  the test early (such as the secret scan).
- **Table-driven** tests where the cases are natural.
- **No real internet.** Anything network-facing is tested against a local
  fake HTTP/SOCKS server on loopback.
- **No secrets in test output.** Never print subscription URLs or tokens;
  fake credentials only.
- **Ktav is pure Go.** Tests that read or write Ktav need no native library,
  no environment variable and no setup; they run anywhere `go test` runs.

## Picking a scope in CI

Manual runs use `workflow_dispatch` inputs:

- `packages` — Go package pattern(s) (default `./...`).
- `tags` — extra comma-separated build tags (default empty).
- `run` — `-run` regex to select individual tests (default: all).
- `os` — `all`, `ubuntu`, `windows` or `macos` (default `all`).
- `race` — enable the race detector (default true).
- `benchtime` — `go test -benchtime` for the bench job, e.g. `1x`,
  `2s`, `5x` (default `1x`).

A push or pull request does not take those inputs: `scripts/ci-scope.sh` picks the
packages from the diff (`setup` job, its choice is in the job summary):

- the packages whose files changed, plus every package that imports them
  (their tests included, from `go list -test`), at all levels and on all OS;
- a change that is not a file below the directory of one Go package (`go.mod`,
  `go.sum`, workflows, scripts, `third_party`, `npm`, `packaging`, the
  installers) runs **everything**; documents (`*.md`, `docs/`, `LICENSE`) run
  no test, except a file that a test reads, listed in
  `scripts/ci-scope-owners.txt` (`docs/cli.md` runs `./cmd/netivri`);
- the line `ci: full` in the message of the head commit runs **everything**
  (a bullet or indentation before it is fine, the case is not); it is the only
  thing the message can ask for. It is how the full run at the end of a sprint
  or before a release is asked for. For a merge to `main` by squash the line
  has to be in the squash message;
- the diff is unknown (a new branch, a forced push): everything.

A manual run of the `CI` workflow (Actions, *Run workflow*) uses the inputs
above and defaults to everything. The coverage floors, the npm checks and the
static Linux builds run only when everything runs (they need the whole
suite); `lint` (which also checks the licenses) always runs. `scripts/test-ci-scope.sh` (in
`lint`) tests the selection.

Each CI job selects its own tags:

- **unit** — always runs untagged; the user `tags` input is ignored
  there, keeping the test hermetic and the coverage floors unit-only.
- **integration** — always adds the `integration` tag plus the user tags. It runs
  only the integration tests (the Test functions of the `integration`-tagged
  files, by name, in the packages that have such files; the unit tests of those
  packages run in the unit job). A manual run with its own `run` regex or
  `tags` runs the whole packages as before.
- **e2e** — adds the `e2e` tag plus the user tags. The e2e tests run one after
  the other and are the longest job, so they are dealt out to three runners per
  OS (`scripts/ci-e2e-shard.sh`: the Test functions of the `e2e`-tagged files,
  one by one in file order; the unit tests of the package are not repeated, the
  unit job runs them). A manual run with its own `run` regex is not sharded.
  The job runs `go test -v` and lists its 20 slowest tests in the job summary,
  which is where to look before moving tests between shards.
- **bench** — runs with the user tags (the job only triggers when `tags`
  contains `bench`). The job runs `go test -bench . -benchmem -benchtime
  {BENCHTIME} -run '^$'` and appends every `Benchmark...` line as a fenced
  code block to the job summary (`-benchmem` allocations/bytes included).
  Benchmarks (all hermetic, loopback port 0 only):

  - `BenchmarkConnectDirect` — one TCP connection, SOCKS5 no-auth handshake,
    CONNECT to a loopback echo backend, 1-byte round trip, close.
  - `BenchmarkConnectRouted` — the same iteration through the sticky-group
    path (authenticator, route of two fake SOCKS5 upstreams), serial.
  - `BenchmarkConnectRoutedParallel` — the same, `b.RunParallel`.
  - `BenchmarkConnectRoutedAccounts` — a routed connect with 1, 100 and
    2000 accounts, the last login in rule order (the worst case of the
    per-account rule scan).
  - `BenchmarkThroughputRouted` — 32 MiB each way over one routed
    connection in 64 KiB writes (`b.SetBytes`, MB/s).
  - `BenchmarkParallelConnects1000` — 1000 concurrent routed
    connect+echo+close per iteration, released together.
  - `BenchmarkStickyChooseHit` / `BenchmarkStickyChooseHitParallel` — the
    pre-bound hit path of the sticky group.
  - `BenchmarkStickyChooseMiss` — selection on a new key, 2 and 20 members.
  - `BenchmarkStickyChooseMissTiered` — the same with the members split
    into two priority tiers (the cost of the tier filter).
  - `BenchmarkWarmNodeProbe` — one real HTTP probe through Mihomo and a
    loopback SOCKS5 upstream whose target emulates network RTT.
  - `BenchmarkSiteKey` — the site-key reduction for a subdomain, a
    multi-label suffix and an IP literal.
  - `BenchmarkHealthRecordDial` — the health table dial recording.
  - `BenchmarkRecord` / `BenchmarkRecordParallel` — enqueueing samples
    into an open stats store.
  - `BenchmarkNodesQuery` — `Nodes` over ~100000 pre-recorded samples of
    50 nodes.
  - `BenchmarkFlushBatch` — 256 samples recorded and flushed per iteration.

Locally equivalent selection is done through env vars understood by
`scripts/ci-test.sh` (not executed locally, only in CI; `CI_VERBOSE=1` adds `-v`
and the list of the slowest tests):

```
CI_PACKAGES=./internal/config/... CI_TAGS=integration CI_RUN=TestParse CI_RACE=1 CI_COVER=1 bash scripts/ci-test.sh
```

## Coverage policy

- Every package has a coverage floor in `scripts/coverage-min.txt`
  (`{PACKAGE_IMPORT_PATH_SUFFIX} {MIN_PERCENT}`, or `default {PCT}`).
- `scripts/check-coverage.sh` parses the Go coverage profile and fails when
  any package is below its floor.
- New code ships with tests in the right scope **in the same change**;
  lowering a floor requires justification in the commit/PR description.
- Floors are only enforced on full runs (`packages == './...'` and no
  `-run` filter); partial runs upload coverage but do not fail floors.
