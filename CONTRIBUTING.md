# Contributing to netivri

Thank you for helping. This page is the short version; the details live in
[`AGENTS.md`](AGENTS.md) (the rules every contributor and AI agent follows),
[`docs/testing.md`](docs/testing.md) and [`docs/cli.md`](docs/cli.md).

## Before you start

- For a bug, open an issue with the bug form. For a larger change, open an
  issue first so the direction is agreed before you write code.
- A vulnerability is not an issue: see [`SECURITY.md`](SECURITY.md).
- Never paste subscription URLs, proxy passwords or tokens anywhere, in issues
  and pull requests included. They are credentials.

## CI is the reference

Builds, tests, benchmarks and install checks run in GitHub Actions on Linux,
macOS and Windows, and a pull request is accepted when CI is green (the
`ci-ok` job summarises it). The maintainers verify only through CI and run
nothing but formatting and static checks locally; AI agents working in this
repository must do the same (see the main rule in [`AGENTS.md`](AGENTS.md)).
The checks are:

```sh
gofmt -l $(git ls-files '*.go' ':(exclude)third_party')
go vet ./...
golangci-lint run        # if installed
```

## What a change contains

- **Tests in the same change**, at the right level: unit (no tag),
  `integration`, `e2e` or `bench`. See [`docs/testing.md`](docs/testing.md)
  for the levels and rules (no sleeps for synchronisation, ports from the
  kernel, no real internet, no secrets in output).
- **Docs in the same change.** A user-facing behaviour change updates
  [`docs/cli.md`](docs/cli.md). Changing the JSON shape or an exit or error
  code is breaking and bumps `schema_version`.
- **A changelog line** under `## [Unreleased]` in
  [`CHANGELOG.md`](CHANGELOG.md) for anything a user would notice.
- **No private data** in the repository: no local paths, user names, hosts or
  tokens.

## Vendored Mihomo

Mihomo is vendored in `third_party/mihomo`. Do not edit it casually: changes go
in as separate commits that follow [`third_party/README.md`](third_party/README.md)
(new code in `netivri_*.go`, a modification notice in every changed upstream
file). `gofmt` is not run on that directory.

## Dependencies and licenses

netivri is `GPL-3.0-only` because it links GPL code, see
[`docs/licenses.md`](docs/licenses.md). A new dependency must carry one of the
allowed licenses; `scripts/check-licenses.sh` (the `licenses` CI job) fails
otherwise. Do not widen the allow-list to make a build pass: a failure needs a
decision recorded in `docs/licenses.md`.

By contributing you agree that your contribution is licensed under
`GPL-3.0-only`.

## Commits and pull requests

- One logical change per commit. The subject is a short imperative sentence
  (`Add the account commands`); the body says what changed and why, in plain
  prose, and why the chosen approach beats the obvious alternative.
- Keep unrelated clean-ups out of a feature or fix.
- A pull request describes the problem, the approach and how CI covers it.
