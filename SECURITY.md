# Security policy

## Supported versions

Only the latest release receives security fixes. While the version is 0.x
there are no backports.

## Reporting a vulnerability

Please do not open a public issue for a vulnerability.

Use GitHub's private reporting: on the repository page open
**Security**, then **Report a vulnerability**. The report stays visible only
to the maintainers until a fix is published.

Include what you can of: the affected version (`netivri version`), the
operating system, what happens, how to reproduce it, and what an attacker
gains. Do not include real subscription URLs, proxy passwords or tokens;
replace them with placeholders.

You can expect an acknowledgement within a week. A confirmed issue is fixed
in a new release and credited in the changelog unless you prefer otherwise.

## What netivri protects

- The proxy listens on `127.0.0.1` unless the user asks for another address.
- The control channel is a named pipe or a Unix socket, never TCP.
- Subscription URLs and `--via` proxies are credentials: they are not printed
  or logged.
- Proxy passwords are stored as argon2id hashes.
- Subscriptions are fetched through a strict proxy and never silently
  directly.

A report that breaks one of these promises is a vulnerability. Problems in an
upstream dependency (Mihomo, `sing`) are welcome too; they are forwarded
upstream and fixed here by updating the vendored copy, see
[`third_party/README.md`](third_party/README.md).

## Verifying a download

Release archives come with `SHA256SUMS` and a build provenance attestation:

```sh
sha256sum -c SHA256SUMS
gh attestation verify netivri_X.Y.Z_linux_amd64.tar.gz --repo PHPCraftdream/netivri
```

npm packages are published with provenance (`npm audit signatures`).
