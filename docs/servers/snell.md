# Snell server

Snell is a single-user TCP server. Each listener routes authenticated traffic to its one configured account. Snell has no standard share URI: `server link` returns a clear no-URI error by default; use `server link --format mihomo` to generate a Mihomo YAML proxy snippet.

## Parameters and secret

| Parameter | Default | Values |
| --- | --- | --- |
| `version` | `2` | `2`, `3` |
| `obfs` | `none` | `none`, `http`, `tls` |
| `obfs_host` | Mihomo client default `bing.com` when obfuscation is enabled | Optional; only with `http` or `tls` |

Unknown parameters and `udp=true` are rejected. UDP is disabled. `tls` refers to Snell obfuscation, which is camouflage and not TLS authentication; no TLS material or certificate-based server authentication is used.

`NewSecret` generates a 32-byte cryptographically random PSK encoded as unpadded base64url. Treat the PSK and generated snippet as credentials. Validation and listener-start errors do not disclose secret values or dependency error text. An empty bind uses the standard `0.0.0.0` default; use a loopback bind for private testing and expose a public listener only intentionally.

## Mihomo client snippet

A snippet contains `type: snell`, `server`, `port`, `psk`, `version`, and `udp: false`; optional `obfs-opts` contains `mode` and `host`. Example shape:

```yaml
proxies:
  - name: edge
    type: snell
    server: edge.example
    port: 1234
    psk: {PSK}
    version: 2
    udp: false
```

Mihomo supports Snell clients through `type: snell`; configure a Mihomo client with a compatible Snell version and options. Surge may be configured manually where the specific release supports the selected Snell version and obfuscation options; no generated Surge configuration or cross-client interoperability guarantee is provided. Netivri emits no URI: `Link` returns a clear no-standard-URI error, and `server link --format mihomo` generates the Mihomo snippet.

## Runtime and coverage

`internal/servers/snell.go` stages a TCP socket and reuses Mihomo's Snell handler through a separately owned accept loop. The upstream handler applies the Snell client ID as `InUser` after inbound additions (`listener/snell/server.go`); netivri's tunnel wrapper pins `InUser` to the configured account. UDP packets are dropped. Protocol `Close` closes the staged TCP listener and waits for its owned accept loop, without calling the dependency listener's `Close`; this permits reconfiguration to drain existing connections. At the higher layer, `internal/core/servers_broker.go` tracks all raw pre-authentication sockets so `Instance.Close` can close them as part of shutdown.

Unit coverage: `internal/servers/snell_test.go`. Loopback integration coverage: `internal/servers/snell_integration_test.go` (integration tag), including routed echo, wrong PSK, global and account allowlists, rotation, and port release. These tests were not run locally. No real-binary A/B e2e test is included; the procedure below is future work.

Future e2e procedure: on server A run `netivri server add --protocol snell --name edge --account alice --port {PORT} --bind 127.0.0.1 --public-host 127.0.0.1 --param version=2`, then obtain the client YAML using `netivri server link edge --account alice --host 127.0.0.1 --format mihomo`. Import the snippet into a second netivri/Mihomo client and echo to a loopback backend. Repeat for version 3 and for `obfs=http` and `obfs=tls`. This is a future procedure, not a reported test result.
