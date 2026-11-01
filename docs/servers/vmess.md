# VMess server

VMess serves multiple resolved accounts on one TCP listener. Authenticated `alice` and `bob` connections carry their login into the core tunnel and use separate account routes. This implementation is plain VMess AEAD over TCP only; TLS, WebSocket, gRPC, and UDP are not enabled.

## Configure

```text
netivri server add --protocol vmess --name edge --account alice,bob --port {PORT} --bind 127.0.0.1 --public-host {HOST}
netivri server link edge --account alice --host {HOST}
netivri server rotate edge --account alice
```

These commands describe the server interface. A loopback bind is private; a reachable bind exposes the endpoint. Treat the generated URI and its UUID as credentials. Store UUID secrets only in the server secrets store, never logs or public output.

## Parameters and secrets

The optional `cipher` link/export parameter defaults to `auto`. Accepted values, case-insensitively: `auto`, `aes-128-gcm`, `chacha20-poly1305`, and `none`. The listener accepts VMess accounts by UUID and AlterID zero; it does not enforce a cipher policy per client. The selected value describes client-side security in links/snippets, not server-side negotiation enforcement.

`none` disables VMess payload encryption; VMess AEAD request/authentication framing remains, but this is not confidentiality. Prefer an encrypted cipher. Each account receives an independently generated random UUID v4. Duplicate UUIDs are rejected because the listener maps a UUID to the authenticated username.

## Links and Mihomo snippets

`Link` emits standard-base64 JSON `vmess://` v2rayN fields: `v`, `ps`, `add`, `port`, `id`, `aid`, `scy`, `net`, `type`, `host`, `path`, `tls`. The link always selects TCP and no TLS. The Mihomo linker emits a matching TCP VMess proxy snippet.

Import only into a client supporting VMess v2 AEAD over TCP. Plain VMess is distinguishable from modern TLS-mimicking protocols and is weaker against probing; prefer VLESS Reality or Trojan where practical. Protect links and rotate UUIDs if exposed.

## Security and future work

The UUID is a bearer credential. Use an explicit client allowlist and a restricted bind where appropriate. A future shared TLS helper may provide a TLS option; it is not implied by links in this implementation.

## Testing

Unit tests cover accepted/rejected parameters, default/case handling, v4 UUIDs, complete URI fields, parsed Mihomo YAML, bad link inputs, and safe error formatting. Tagged loopback integration coverage uses both accounts on one listener, checks separate routing, rotates Alice's UUID, checks global and account client filters and drop counters, and checks listener port release. Tests run only in CI.
