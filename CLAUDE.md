# CLAUDE.md

Read [`AGENTS.md`](AGENTS.md) — it is the source of truth for this repo.

## Main rule

All compilation, tests, benchmarks, packaging and install checks run
**only in GitHub Actions CI**. Locally only linters and syntax checks are
allowed (`gofmt`, `go vet`, `golangci-lint`, script/config syntax checks).
Never run `go build`, `go test`, `go run`, `go install`, `npm pack`/`npm test`
or the installer scripts on a local machine.
