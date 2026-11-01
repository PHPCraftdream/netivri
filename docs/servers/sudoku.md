# Sudoku server

Sudoku is a single-user, TCP server. Each server spec names exactly one resolved account. The listener applies `inbound.WithInName(server)` and `inbound.WithInUser(login)`, so accepted TCP sessions use that account's normal route and accounting.

## Parameters

All parameters are optional; unknown or invalid values are rejected with a sanitized error naming the failing parameter. Defaults below are Mihomo's vendored Sudoku defaults.

| Parameter | Default | Meaning |
| --- | --- | --- |
| `table-type` | `prefer_entropy` | `prefer_ascii`, `prefer_entropy`, `up_ascii_down_entropy`, or `up_entropy_down_ascii`. |
| `padding-min` | `10` | Minimum padding insertion ratio, integer `0..100`. |
| `padding-max` | `30` | Maximum padding insertion ratio, integer `0..100` and at least the effective minimum. |
| `enable-pure-downlink` | `true` | Enables pure downlink framing. |
| `disable-http-mask` | `false` | `true` disables HTTP masking. |
| `http-mask-mode` | `legacy` | `legacy`, `stream`, `poll`, or `auto`. |

Custom pattern tables and path roots are intentionally not exposed: Mihomo's table constructor has validation/errors that can contain configuration material, and safe shape validation is not part of this interface. No mux extension is exposed. The generated configuration includes only explicitly supplied parameters.

Sudoku has no standard share URI; `server link --format uri` reports that a URI is unavailable and directs users to `--format mihomo`. Mihomo YAML is emitted with typed numbers and booleans, not quoted strings.

## Secrets and links

`NewSecret` generates 32 CSPRNG bytes, encoded as unpadded base64url in the account's `users.key` secret field. No additional server-only material is needed. Secrets belong only in the private server secret store; a Mihomo YAML snippet contains the credential and must be delivered privately.

Example shape (the key is intentionally a placeholder):

```yaml
proxies:
  - name: edge
    type: sudoku
    server: vpn.example.org
    port: 8388
    key: {GENERATED_KEY}
    udp: false
```

Mihomo's named outbound adapter provides the authoritative client configuration. Compatible applications that parse Mihomo proxy YAML can use the snippet; the generic `common/convert.ConvertsV2Ray` URI importer has no Sudoku case, so do not use URI import for Sudoku. Configure the returned Mihomo snippet directly.

## Security and transport

The listener supports TCP sessions and UDP-over-TCP; netivri's core server tunnel drops and counts relayed UDP packets. The snippet sets `udp: false`. A key grants access to the configured account route. Bind defaults to `0.0.0.0`; use `127.0.0.1` for local-only testing. Protocol handshake failures are not accompanied by key-bearing netivri errors; Mihomo listener internals may log remote addresses.

The prepared socket is wrapped so listener shutdown synchronizes the vendor accept loop's error path before it checks the vendor `closed` flag.

## Verification scope

Unit coverage checks validation, key format and uniqueness, redaction, typed client configuration, and confirms that no URI importer exists. CI integration coverage starts a core instance, uses Mihomo's matching Sudoku outbound against a loopback recorder and echo backend, and checks routing, wrong-key refusal, account allowlist drops, key rotation, and port release. No compile or test command is run locally.

For a later real-binary e2e task, the flow is `server add --protocol sudoku --name {NAME} --account {ACCOUNT} --port {PORT} --bind 127.0.0.1 --public-host {HOST}`, then `server link {NAME} --host {HOST} --format mihomo`, and supply the snippet to Mihomo.
