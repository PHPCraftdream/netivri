# Trojan server

Trojan is a multi-user TLS/TCP endpoint. Its clients authenticate with a unique netivri-generated 32-byte random password; Mihomo identifies the authenticated username for account routing. UDP is disabled; the core wrapper drops UDP attempts.

## Parameters and generated values

Allowed parameters: `public_host`, `sni`, `alpn`, `cert_file`, `key_file`. Unknown keys, including fallback options, are rejected. `public_host` and `sni` must be a DNS name or IP. `alpn` is a comma-separated list of printable non-space ASCII tokens (1–255 bytes each); the server listener has no ALPN configuration field, so this config only advertises ALPN in client links/snippets and does not change server negotiation. Certificate and key paths must be supplied together; they resolve against the process working directory, then Mihomo applies SAFE_PATHS.

Generated material selects `public_host`, else valid `sni`, else the TLS helper's localhost default. It is a self-signed P-256 certificate, private key and SHA-256 leaf fingerprint. The private key and account passwords belong only in the protected server-secrets store. Passwords are 32 random bytes in unpadded URL-safe base64; `NewSecret` validates protocol parameters too.

## Links and client import

If `sni` is omitted, the link uses the client-facing link host as SNI; if `alpn` is omitted, the client uses its own default ALPN behavior. For CA-trusted deployments, the supplied certificate SAN must cover the configured SNI. Mihomo's Trojan URI importer reads certificate pins from `pcs`; the generated link uses `pcs` for the leaf SHA-256 fingerprint. It also carries TCP transport, SNI, ALPN, and certificate verification enabled. Mihomo's URI importer sets `udp: true` by default, but Trojan server UDP is not supported by netivri: the core wrapper drops UDP attempts. The Mihomo YAML snippet explicitly sets `udp: false`.

The Mihomo snippet uses `type: trojan`, `fingerprint` set to the leaf hash, `skip-cert-verify: false`, `udp: false`, and emits configured `alpn` as client ALPN.

## Security and testing

A URI is a credential and must be shared only with its intended user. Spec formatting and errors redact passwords, PEM and private keys; TLS fingerprints are not secret. Rotation invalidates prior passwords; certificate rotation also invalidates existing pins and requires redistributing links. There is no ACME or automated file certificate renewal.

Unit tests cover parameter validation, random password format, material/SAN generation, link and snippet parsing, and redaction. Integration tests drive the server with Mihomo's Trojan outbound for pin and test-CA verification, verify account routing, wrong-password rejection, client filtering, rotation and listener release. Tests run in CI, not locally.
