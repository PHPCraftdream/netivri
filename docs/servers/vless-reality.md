# VLESS Reality server

VLESS Reality is a multi-user TCP listener. Each configured account has its own generated UUID and is routed by the authenticated VLESS username through that account's normal netivri route. Reality uses the server's X25519 private key and short IDs; it does not use a server certificate. This is protocol documentation for the current implementation, not a claim that runtime integration tests have passed.

## Configure and run

The command syntax describes the product interface (which may not be implemented in every checkout):

```text
netivri server add --protocol vless-reality --name edge --account alice,bob --port {PORT} --bind 127.0.0.1 --public-host {HOST}
netivri server link edge --account alice --host {HOST}
netivri server rotate edge --account alice
```

`{PORT}` is a free TCP port. `{HOST}` is the client-reachable public DNS name or address, not necessarily the bind address. The generated link grants access as that account and is a credential: keep it and the secrets file private.

Parameters are optional:

| Parameter | Default | Meaning |
| --- | --- | --- |
| `dest` | `www.microsoft.com:443` | Reachable TLS camouflage target, with explicit port. |
| `sni` | host portion of `dest` | TLS server name offered by the client and accepted by the Reality listener; must be a DNS hostname. |
| `flow` | `xtls-rprx-vision` | The only supported flow. |
| `fingerprint` | `chrome` | Supported Reality set: `chrome`, `firefox`, `safari`, `ios`, `edge`, `random`, `randomized`. Mihomo's mapped Android hello lacks a TLS 1.3 key share. Older aliases are excluded by policy; this does not claim every excluded hello is incompatible. |

The destination must be reachable from the server and support TLS 1.3 with X25519. It is a camouflage target, not the server's own domain; configure a reachable host and appropriate SNI. No domain ownership or certificate for the listener is required. The `sni` parameter must be a DNS hostname; when `dest` is an IP literal, set `sni` explicitly to a DNS name.

## Generated secrets

`NewMaterial` returns `private_key`, `public_key` (base64url without padding) and `short_ids` (three comma-separated random eight-hex-character IDs). Per-account `NewSecret` returns `uuid`. These values are stored in the server secrets material and user maps, respectively. Rotation replaces account UUIDs or the Reality material as requested. Never expose the private key, UUIDs, or generated links in logs or public output.

## Client configuration

`server link` emits a URI with UUID, TCP transport, `security=reality`, flow, SNI, fingerprint, public key (`pbk`) and first short ID (`sid`). Its shape is:

```text
vless://{UUID}@{HOST}:{PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni={SNI}&fp=chrome&pbk={PUBLIC_KEY}&sid={SHORT_ID}&type=tcp#{NAME}
```

The placeholders are generated values; the actual URI percent-encodes query values. `server link --format mihomo` emits a Mihomo proxy YAML entry with the equivalent settings (`type: vless`, `tls: true`, `network: tcp`, `reality-opts.public-key` and `short-id`).

v2rayN and Hiddify clients that support VLESS Reality can import the URI from clipboard. Xray-core itself uses a manual JSON client configuration rather than importing this URI; configure a VLESS outbound with the same UUID, server/port, TCP transport, TLS Reality public key and short ID, SNI, fingerprint and Vision flow. sing-box support varies by version and URI importer; when the URI is not accepted, configure its VLESS outbound manually with the same values.

This implementation supports TCP only; it does not provide UDP. Reality's active-probe resistance is not anonymity, does not conceal traffic from the endpoint or network observer, and is not a guarantee against detection or blocking.

## Logging

The vendored Mihomo Reality builder sets `Log = log.Debugln`; Reality/uTLS creates debug events containing authentication key bytes and client short IDs even while Mihomo's log level is `SILENT` (core sets that level). This repository has no subscriber and writes no events to storage or output today. Any future subscriber must filter secret-bearing lines.

## Verification status and implementation notes

The loopback integration test is CI-only under the `integration` build tag. Its local TLS 1.3 camouflage target uses a large RSA certificate and a 4096-byte randomized `SessionState.Extra` ticket extension to provide record-size margin against Reality's regenerated handshake messages. The ticket padding uses Go's `WrapSession`/`EncryptTicket`; the standard server-side TLS 1.3 ticket encoding does not include the server certificate. This test fixture is not a runtime certificate requirement or a confirmed explanation for any CI failure.

Source inspection of vendored Mihomo v1.19.32 found Reality `Config.Build` uses `inner.HandleTcp` for camouflage. That tunnel metadata is `C.INNER` without `SrcIP` or `InUser`; when global `listen.allowed_clients` is configured, the shared server policy refuses the camouflage connection because it has no client address. Thus unauthenticated visitors (including allowlisted sources) are dropped instead of camouflaged; without a global list this filter does not refuse it. Do not assume valid clients are unaffected, since source flow may send handshakes through that target. The integration exercises unrestricted global policy, then an Alice account policy.


## Decisions

Consider patching the vendored Reality builder to drop the callback, following `third_party/README.md`'s patch process.

The Reality listener's lifecycle and race behavior during shutdown has not been dynamically verified; CI race-enabled integration results are needed before drawing conclusions. Do not interpret the plain `closed` field in isolation as proof of a race, since the listener wrapper and its accept path may provide synchronization. This change does not patch vendored Mihomo.
