# Server protocols

`netivri server` manages registered inbound endpoints. Each authenticated connection is assigned to its account and uses that account's normal route. See the [server CLI reference](cli.md), [main CLI contract](../cli.md#server), and [configuration reference](../config.md#server-listeners).

| Registered protocol | Transport | Accounts | Link format | Certificate pin carrier | Notes |
| --- | --- | --- | --- | --- | --- |
| [anytls](anytls.md) | TCP | multi | URI or Mihomo YAML | URI `hpkp`; YAML `fingerprint` | TLS; UDP relay is dropped. |
| [hysteria2](hysteria2.md) | UDP (QUIC) | multi | URI or Mihomo YAML | URI `pinSHA256`; YAML `fingerprint` | UDP listener; relay payloads are dropped. |
| [mieru](mieru.md) | TCP | multi | URI or Mihomo YAML | none | TCP only here; URI `mierus://`. |
| [shadowquic](shadowquic.md) | UDP (QUIC) | multi | Mihomo YAML only | none | JLS authentication; no certificate pin. |
| [shadowsocks](shadowsocks.md) | TCP | single | URI or Mihomo YAML | none | UDP unsupported. |
| [snell](snell.md) | TCP | single | Mihomo YAML only | none | No standard URI. |
| [sudoku](sudoku.md) | TCP | single | Mihomo YAML only | none | UDP relay disabled in generated YAML. |
| [trojan](trojan.md) | TCP | multi | URI or Mihomo YAML | URI `pcs`; YAML `fingerprint` | TLS; disable client UDP relay. |
| [trusttunnel](trusttunnel.md) | TCP | multi | Mihomo YAML only | YAML `fingerprint` | Requires HTTP/2; no URI importer. |
| [tuic](tuic.md) | UDP (QUIC) | single | URI or Mihomo YAML | Mihomo YAML `fingerprint`; URI unsupported | URI import does not carry certificate pin. |
| [vless-reality](vless-reality.md) | TCP | multi | URI or Mihomo YAML | none | Reality uses a public key, not a certificate pin. |
| [vmess](vmess.md) | TCP | multi | URI or Mihomo YAML | none | Plain VMess AEAD over TCP. |

`multi` listeners can serve multiple accounts with distinct credentials. `single` listeners serve exactly one account each. A link contains credentials; share it privately. `server link --format mihomo` is the authoritative form where URI import lacks settings or pin support. TLS protocols generate self-signed material unless `cert_file` and `key_file` are supplied; see [TLS material](tls.md).

## Accounts, filtering, and lifecycle

Server traffic follows the configured account route, including direct/post-hop behavior for accounts without a route. `listen.allowed_clients` is enforced together with an account's optional `allowed_clients`: the client must pass both rules. An absent account rule imposes no extra restriction. Account allowlist-only changes apply hot to server policy without listener restart; existing connections survive, including mixed authentication and server endpoints. Global `listen.allowed_clients`, credentials, account membership, bind, port, and other server changes require listener restart/reconfiguration.

Generated passwords, UUIDs, keys, private keys and other material are stored in `{HOME}/server-secrets.ktav` with owner-only permissions, separate from `config.ktav`. Configuration and secret displays redact parameter values and credentials. A generated URI or Mihomo snippet intentionally contains credentials; protect its output. `server rotate` replaces user credentials or, without `--account`, server material and all user secrets.

A loopback server may bind `127.0.0.1` but advertise `::ffff:127.0.0.1` in `public_host`/`--host` when creating a local share link. The subscription parser drops banners advertising `127.0.0.1`, `0.0.0.0`, `::1`, or `localhost`; the mapped IPv4 address remains local. The server listener must still bind to `127.0.0.1`.

QUIC listeners bind UDP sockets. A changed UDP-bound listener closes before its replacement binds, briefly interrupting service and ending existing sessions; socket release is synchronous. Failed replacement restores the previous UDP listener best-effort. UDP relay payloads are dropped and counted, even on UDP-bound protocols. The core binds sockets by transport, so TCP and UDP can share a port number. However, current config validation and `server add` reject overlapping bind/port pairs regardless of transport; the CLI does not support sharing that port across server transports. The distinction is reported as a configuration/CLI limitation, not a supported setup.

## Testing

Unit tests validate configuration, links, and filtering without opening sockets. Integration tests use the embedded core with a matching Mihomo client connected over loopback. E2e tests run two real angels using a generated link or subscription; these run only in CI. Mieru and shadowquic have no matching e2e test files. See [testing strategy](../testing.md).
