# ShadowQUIC server

ShadowQUIC provides multi-account, JLS-authenticated QUIC proxying over UDP. Each account receives a separate randomly generated 32-byte password. Authenticated JLS identity is attached to inbound traffic and maps to the matching account route.

## Parameters

| Parameter | Default | Meaning |
|---|---|---|
| `sni` | `localhost` | Optional JLS camouflage server name override; when omitted, server and client use the same `localhost` default, independently of link host. |
| `public_host` | unset | Certificate-generation hostname; the shared TLS helper falls back to `sni`, then localhost. |
| `cert_file`, `key_file` | generated self-signed P-256 certificate | Provide together to use external TLS material. |
| `alpn` | `h3` | Comma-separated ALPN identifiers; included in the Mihomo snippet when configured. |
| `quic_versions` | Mihomo ShadowQUIC defaults | Comma-separated versions accepted by Mihomo's ShadowQUIC parser; included in the Mihomo snippet when configured. |
| `max_idle_time` | 30000 ms | Positive session idle timeout. |
| `max_datagram_frame_size` | 1400 | Integer from 1024 through 2147483647. |
| `recv_window_conn`, `recv_window` | Mihomo TUIC defaults | Optional positive stream and connection receive windows. |
| `disable_mtu_discovery` | false | Disable QUIC path MTU discovery. |
| `congestion_controller` | `bbr` | Optional `bbr`, `cubic`, or `new_reno` congestion controller. |

Unknown parameters are rejected. Open the configured endpoint for UDP; QUIC TCP streams route normally through the account. UDP association payloads are not relayed: the server omits the UDP handler and UDP routed through server tunnels is rejected by the core. Keep generated secrets private; TLS private material belongs only in the protected secrets store. JLS-authenticated TLS deliberately skips certificate-chain and external certificate verification, so certificate fingerprints are neither required nor supported as a pinning mechanism.

## Client snippet

ShadowQUIC has no standard URI; `server link --format mihomo` emits a `type: shadowquic` outbound snippet with server, port, username, password and SNI. SNI defaults to localhost independently of link host. Configured ALPN and QUIC versions are also included. Mihomo's ShadowQUIC outbound accepts the generated fields. The password is a credential. The snippet disables UDP relay. Mihomo is the supported client documented here; compatibility with other clients has not been verified.

The server loads the certificate/key pair when its listener starts; replacing external files takes effect after the listener is restarted. JLS-authenticated connections intentionally skip certificate-chain and external certificate verification, so certificate fingerprints are neither required nor supported as a pinning mechanism. JLS authentication remains required. The server's camouflage-forwarding facility is disabled. UDP association payloads are not relayed: the server has no UDP handler, and UDP routed through server tunnels is rejected by the core.

## Lifecycle and verification

The integration test covers routed TCP traffic, authentication failures, allowed-client filtering, secret rotation and listener close. Unit coverage includes validation, certificate material and SANs, safe errors and formatting, plus generated YAML acceptance by Mihomo's adapter and the subscription parser (including retained ALPN/version fields). These unit checks construct an outbound but do not dial or open sockets.

The e2e test (`cmd/netivri/server_e2e_shadowquic_e2e_test.go`) follows this procedure, which also works by hand: start server A with `netivri server add --name edge --protocol shadowquic --account {ACCOUNT} --port {PORT} --bind 127.0.0.1 --param sni=localhost --public-host ::ffff:127.0.0.1`, then obtain the client config with `netivri server link edge --account {ACCOUNT} --host ::ffff:127.0.0.1 --format mihomo`. Serve that YAML on loopback as a subscription; configure angel B to consume the loopback subscription URL and route through the imported node. The subscription parser rejects localhost banners and the exact `127.0.0.1` server literal; the mapped IPv6 literal avoids that rule while keeping the listener bound to loopback. `--format mihomo` belongs to `server link`, not the subscription consumer. ShadowQUIC has no standard URI.
