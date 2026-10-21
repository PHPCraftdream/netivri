## What and why

<!-- The problem, and why this approach. -->

## Checklist

- [ ] Tests for the change, at the right level (see `docs/testing.md`)
- [ ] `docs/cli.md` updated for any user-facing change (`schema_version` bumped if the JSON or a code changed)
- [ ] Line added under `## [Unreleased]` in `CHANGELOG.md`
- [ ] `gofmt` and `go vet` are clean; I read the CI result
- [ ] No subscription URLs, passwords, tokens or local paths in the diff
- [ ] New dependencies pass `scripts/check-licenses.sh`
