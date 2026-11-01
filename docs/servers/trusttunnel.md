# TrustTunnel server

TrustTunnel is registered as a multi-user TCP server. Mihomo's listener authenticates HTTP Basic credentials, stores the authenticated username in the request context (`third_party/mihomo/transport/trusttunnel/service.go`), and its sing handler converts that context identity to `inbound.WithInUser` (`third_party/mihomo/listener/sing/context.go`). Netivri therefore configures the account login as the username and a separate generated password per account.

## Configure

The CLI syntax below describes the server interface:

```text
netivri server add --protocol trusttunnel --name edge --account alice,bob --port {PORT} --bind 127.0.0.1 --public-host {HOST}
netivri server link edge --account alice --host {HOST} --format mihomo
netivri server rotate edge --account alice
```

Empty bind defaults to `0.0.0.0`; prefer loopback while testing and expose a reachable bind only intentionally. Sharing a generated password grants that account's route.

Supported parameters:

| Parameter | Default | Meaning |
| --- | --- | --- |
| `public_host` | none | Certificate SAN host and link host fallback; `server link --host` overrides it. |
| `sni` | selected link host | TLS server name clients verify. |
| `alpn` | `h2` | Only `h2` is accepted; the outbound requires HTTP/2 for TCP mode. |
| `cert_file`, `key_file` | generated material | Must be provided together. Existing matching certificate/key files use the shared TLS helper. |
| `network` | `tcp` | Only TCP is accepted. |
| `udp` | `false` | `true` is rejected. |
| `quic` | `false` | `true` is rejected. |

Unknown keys are rejected without echoing parameter values. Without external files, `NewTLSMaterial` generates a self-signed ECDSA P-256 certificate and SHA-256 fingerprint using `public_host`, then `sni`, then its localhost default. No ACME integration exists.

`NewSecret` creates 32 cryptographically random bytes encoded as unpadded base64url in the `password` field. `Start` constructs the listener's users map as account login to generated password, preserving the routing identity in `InUser`. Listener startup errors redact the dependency error while retaining `errors.Is` classification.

## Mihomo client snippet

There is no TrustTunnel URI parser in Mihomo's `common/convert` importer. `Link` returns an explicit error rather than inventing a URI. `Mihomo` emits a YAML proxy snippet using Mihomo's actual adapter type `trusttunnel` and its `username`, `password`, `sni`, and `fingerprint` fields. The snippet explicitly disables UDP and QUIC. The fingerprint pins the generated leaf certificate; it is not a secret. Protect the generated password and snippet as credentials.

TCP connections authenticate and route as the named account. Core's server tunnel enforces global and account allowlists before forwarding, counts denied peers, and drops UDP payloads. QUIC is intentionally not enabled: it requires a UDP-bound listener and is outside this TCP-only implementation.

Mihomo client users can paste the emitted proxy entry under their `proxies:` configuration (with a matching proxy group as appropriate). The profile authenticates using the account login and generated password. Keep both the snippet and password confidential.

Mihomo accepts a matching leaf fingerprint pin while bypassing ordinary certificate-chain, hostname, and expiry checks. Obtain the pin through a trusted out-of-band channel, monitor certificate expiry, and redistribute the updated snippet after certificate rotation; never use `skip-cert-verify`.

To import the snippet into another netivri on the same machine, advertise a host the subscription parser accepts: it drops nodes whose server is exactly `127.0.0.1` or `localhost` as provider banners, so use `--public-host ::ffff:127.0.0.1` while binding `127.0.0.1`.


Unit tests cover parameter validation, secret encoding/uniqueness, TLS SAN selection and precedence, redaction in `%v`, `%+v`, and `%#v` formats, explicit URI unavailability, converter rejection of the absent TrustTunnel URI scheme, IPv6 server snippets, YAML decoding, and the matching Mihomo `adapter.ParseProxy` constructor. The integration-tagged loopback test starts core on an ephemeral port, trusts TLS after startup, drives both account credentials with Mihomo's TrustTunnel outbound, checks distinct account routes, wrong-password rejection, global and per-account client denies, password rotation and port release. It uses deadlines and drains server connections before close.

This repository forbids local compilation and test runs. Run the package's unit and `integration` CI scopes for verification.
