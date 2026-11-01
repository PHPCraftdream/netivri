# Shadowsocks server

Shadowsocks is the first `internal/servers` protocol: one listener serves exactly one account. This version supports TCP only; UDP is unsupported.

## Configure and run

The server package accepts resolved specs; the example command syntax below describes the planned product interface, not a claim that the CLI command is implemented:

```text
netivri server add --protocol shadowsocks --name edge --account alice --port {PORT} --bind 127.0.0.1 --public-host {HOST}
netivri server link edge --host {HOST}
netivri server rotate edge --account alice
netivri server list
netivri server remove edge
```

`{PORT}` is a free TCP port, and `{HOST}` is the public DNS name or address clients use. The explicit loopback bind keeps the example private; use a reachable bind only when intentionally exposing the service. Sharing a generated link grants its recipient the ability to use Alice's account route. Treat the link as a credential: do not log it or put it in public output.

`Spec.ListenAddress()` defaults an empty bind to `0.0.0.0`. `Spec.Validate()` requires a server name of `1..32` characters from `A-Za-z0-9._-`, a port in `1..65535`, and exactly one resolved login of `1..64` characters from `A-Za-z0-9._@-`; it rejects `*` and duplicate logins. It does not choose a port. `Start`, `Link`, and `Mihomo` reject specs whose protocol is not `shadowsocks`.

## Parameters and secrets

Only these cipher values are accepted (case-insensitively):

| Cipher | Key bytes |
| --- | ---: |
| `2022-blake3-aes-128-gcm` | 16 |
| `2022-blake3-aes-256-gcm` | 32 |
| `2022-blake3-chacha20-poly1305` | 32 |
| `aes-128-gcm` | 16 |
| `aes-256-gcm` | 32 |
| `chacha20-ietf-poly1305` | 32 |

The default is `2022-blake3-aes-128-gcm`. `NewSecret` returns `{ "password": ... }`; key bytes are cryptographically random. For 2022 methods the stored key is standard padded base64 for Mihomo's 2022 password-based server constructor. For the three AEAD methods the generated key bytes are stored as unpadded base64url and the resulting printable password string is passed through unchanged. `udp` may be omitted or set to `false`; `udp=true` is rejected because UDP through account routes is not supported yet. Other `udp` values are invalid.

Server-side `Spec.String()` and `Spec.GoString()` redact `Params`, `Material`, and `Users`. Listener-construction errors redact dependency messages; endpoint secrets must never be included in logs or status output. A generated client URI intentionally contains its credential.

## Links and Mihomo snippets

`Link` emits SIP022 plaintext `METHOD:PERCENT-ENCODED-PASSWORD` userinfo for the three `2022-blake3-*` methods; the password is escaped with `url.QueryEscape`. The three legacy AEAD methods use SIP002 unpadded base64url of the exact `cipher:password` text. The fragment is path-escaped and `net.JoinHostPort` brackets IPv6 hosts. The link's host argument is used as supplied, so pass the client-reachable `{HOST}` rather than a bind address when they differ.

`shadowsocks` additionally implements the optional `MihomoLinker` interface:

```go
Mihomo(spec Spec, login, host string) (string, error)
```

It returns a proxy YAML snippet with the same endpoint, cipher and stored password, and always sets `udp: false`.

## Runtime behavior and current coverage

Startup pre-acquires only the TCP socket before passing it to Mihomo, with UDP disabled. If Mihomo construction fails, the unclaimed TCP socket is closed. The core socket broker stages listener leases transactionally: unchanged specs retain their listener and connections; a changed listener stops receiving new connections after commit, while already tracked connections can drain until `Instance.Close`. This describes the current code path, not a claim of runtime verification in this change.

UDP is unsupported: the core tunnel wrapper drops and counts UDP traffic arriving through all server tunnels. Mihomo's UDP-incompatible account rule may fall through to `DIRECT` for UDP rather than the account route, so UDP must fail closed rather than be sent direct. The Shadowsocks Mihomo snippet therefore sets `udp: false`.

Unit tests cover registry behavior, Spec validation/redaction/equality, cipher validation and generated key sizes, SIP002/SIP022 payload round-trips, and safe startup-error formatting/classification. `/internal/core/servers_integration_test.go` contains the loopback TCP route, rotation, policy, listener-reuse, and close coverage; it is an integration-tagged CI test and was not run for this documentation change. There is no Shadowsocks UDP packet loopback test yet.