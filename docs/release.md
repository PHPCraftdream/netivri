# Release process

## Cutting a release

1. Make sure `main` is green in CI.
2. Add a `## [X.Y.Z] - YYYY-MM-DD` section to [`CHANGELOG.md`](../CHANGELOG.md)
   and push it. The workflow takes the text of the GitHub Release from that
   section and stops at the first step when it is missing or empty.
3. Tag and push: `git tag vX.Y.Z`, then push the tag to `origin`.
4. The `release` workflow then
   - builds six archives (windows/linux/darwin x amd64/arm64) and the six
     platform npm packages plus the main one;
   - installs them on Linux, macOS and Windows (installers and `npm i -g`);
   - runs the dependency license check (`scripts/check-licenses.sh`);
   - generates `SHA256SUMS` and attests the build provenance of the archives;
   - publishes the GitHub Release with the archives, the checksums and the
     changelog section as its text;
   - publishes the seven npm packages (see below).

The version embedded in the binary (`netivri status` shows it) is the tag
name without the leading `v`. A tag with a prerelease part (`v0.2.0-rc.1`)
makes a GitHub pre-release and goes to the npm dist-tag `next`, not `latest`.

## Dry run

Dispatch the workflow manually (`workflow_dispatch`) to build and upload
artifacts **without** publishing a release or attesting provenance. The
version is `0.0.0-dryrun`. The npm step runs `npm publish --dry-run` for all
seven packages, which checks the packed tarballs without writing to the
registry. Use it to check packaging before tagging.

## Publishing to npm: token or Trusted Publishing

The `npm-publish` job picks the way to authenticate on every run:

| `NPM_PUBLISH` variable | `NPM_TOKEN` secret | Result |
|---|---|---|
| `off` | any | the npm step is skipped |
| anything else | set | publishes with the token |
| anything else | not set | publishes through Trusted Publishing (OIDC) |

Both ways attach a provenance statement to every package. The six platform
packages go first and the main package last; a version that is already on the
registry is skipped, so re-running a failed release finishes it.

Creating several new packages within a minute can make npm refuse one with
`E403 Package name triggered spam detection` (it happened on the first
release). Wait a while and re-run the failed `publish to npm` job (`gh run
rerun {RUN_ID} --failed`); if npm keeps refusing, ask npm support to release
the name. A re-run uses the scripts of the tagged commit.

**First release: token.** Trusted Publishing can only be configured for a
package that already exists on npm, so the first publish of the seven packages
needs a token. Create an npm access token that may publish and store it as the
repository secret `NPM_TOKEN` (Settings, Secrets and variables, Actions). The
workflow writes it to a temporary `.npmrc` that reads it from the
environment; it is never printed.

**Switching to Trusted Publishing.** After the first release is on npm:

1. For each of the seven packages (`netivri`, `netivri-linux-x64`,
   `netivri-linux-arm64`, `netivri-darwin-x64`, `netivri-darwin-arm64`,
   `netivri-win32-x64`, `netivri-win32-arm64`) open the package on npmjs.com,
   Settings, Trusted Publisher, and choose GitHub Actions with owner
   `PHPCraftdream`, repository `netivri`, workflow filename `release.yml` and
   an empty environment.
2. Delete the `NPM_TOKEN` secret and revoke the token on npm.
3. Cut the next release. With no secret the job runs in OIDC mode: no token is
   stored anywhere, and npm checks that the publish comes from that workflow.

Trusted Publishing needs npm 11.5.1 or newer on Node 22.14 or newer; the job
installs a new enough npm itself. To protect the publish with a manual
approval, put the job into a GitHub environment with required reviewers, write
that environment name into the Trusted Publisher settings and add
`environment:` to the job.

## Verifying a release

```sh
# the archive was built by this repository's release workflow
gh attestation verify netivri_X.Y.Z_linux_amd64.tar.gz --repo PHPCraftdream/netivri

# provenance and signatures of the installed npm packages
npm audit signatures
```

## What each archive contains

`netivri_{VERSION}_{GOOS}_{GOARCH}.zip` (windows) or `.tar.gz` (others):

- `netivri` / `netivri.exe` — the Go binary, built with `CGO_ENABLED=0`.
- `LICENSE`, `README.md`.
- `THIRD_PARTY_LICENSES/` — license texts of the linked Go modules (see
  `docs/licenses.md`).

The archive holds no native library: the Ktav Go binding runs its pure-Go
backend inside the binary (see `docs/design/config-ktav.md`), so nothing is
loaded, downloaded or fetched at run time.

## Verifying checksums

```sh
sha256sum -c SHA256SUMS
```

inside the directory holding the archives and the checksum file.

## Consumers

The npm platform packages and the standalone installer (later tasks) consume
these same archives; they repackage, they do not rebuild.

## Known limitation

musl-based Linux distributions such as Alpine are not supported: the Linux
release is built and tested for glibc only.
