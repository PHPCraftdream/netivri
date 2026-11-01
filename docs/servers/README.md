# Server protocols

`netivri server` manages registered inbound endpoints. Each authenticated connection is assigned to its account and uses that account's normal route. See the [server CLI reference](cli.md), [main CLI contract](../cli.md#server), and [configuration reference](../config.md#server-listeners).

| Registered protocol | Transport | Accounts | Link format | Certificate pin carrier | Notes |
| --- | --- | --- | --- | --- | --- |
| [anytls](anytls.md) | TCP | multi | URI or Mihomo YAML | URI `hpkp`; YAML `fingerprint` | TLS; UDP relay is dropped. |
| [hysteria2](hysteria2.md) | UDP (QUIC) | multi | URI or Mihomo YAML | URI `pinSHA256`; YAML `fingerprint` | UDP listener; relay payloads are dropped. |
| [mieru](mieru.md) | TCP | multi | URI or Mihomo YAML | none | TCP only here; URI `mierus://`. |
| [shadowquic](shadowquic.md) | UDP (QUIC) | multi | Mihomo YAML only | none | JLS authentication; no certificate pin. |
| [shadowsocks](shadowsocks.md) | TCP | single | URI or Mihomo YAML | none | 2022 ciphers can share; legacy AEAD is exclusive; UDP unsupported. |
| [snell](snell.md) | TCP | single | Mihomo YAML only | none | No standard URI. |
| [sudoku](sudoku.md) | TCP | single | Mihomo YAML only | none | UDP relay disabled in generated YAML. |
| [trojan](trojan.md) | TCP | multi | URI or Mihomo YAML | URI `pcs`; YAML `fingerprint` | TLS; disable client UDP relay. |
| [trusttunnel](trusttunnel.md) | TCP | multi | Mihomo YAML only | YAML `fingerprint` | Requires HTTP/2; no URI importer. |
| [tuic](tuic.md) | UDP (QUIC) | single | URI or Mihomo YAML | Mihomo YAML `fingerprint`; URI unsupported | URI import does not carry certificate pin. |
| [vless-reality](vless-reality.md) | TCP | multi | URI or Mihomo YAML | none | Reality uses a public key, not a certificate pin. |
| [vmess](vmess.md) | TCP | multi | URI or Mihomo YAML | none | AEAD; alterId is zero. |

`multi` listeners can serve multiple accounts with distinct credentials. `single` listeners serve exactly one account each. A link contains credentials; share it privately. `server link --format mihomo` is the authoritative form where URI import lacks settings or pin support. TLS protocols generate self-signed material unless `cert_file` and `key_file` are supplied; see [TLS material](tls.md).

## Shared TCP endpoints

| Eligibility | Requirement |
| --- | --- |
| TLS family: Trojan, AnyTLS, TrustTunnel, VLESS-Reality | Protocol-owned explicit SNI/ALPN match keys; SNI/default matching rules below. |
| Shadowsocks 2022 | Any of its three ciphers; trial classification enabled. One PSK, listener and account. |
| VMess | AEAD (`alterId=0`); trial classification enabled; at most 1024 users. |
| Legacy Shadowsocks AEAD | Its wire format is not supported by the bounded trial classifier; exclusive. |
| Snell, Sudoku, Mieru | No trial classifier; exclusive. |
| UDP-bound protocols | No UDP trial classifier; exclusive for UDP transport. UDP-bound and TCP listeners may use the same port number. |

Across a shared endpoint, raw PSKs and VMess UUIDs must be distinct; at most 16 trial servers are allowed. Reused credentials and trial default members fail with `SERVER_PORT_AMBIGUOUS` (exit 6). A same-transport exclusive conflict returns `PORT_IN_USE` (exit 6); ambiguous shared-port classification returns `SERVER_PORT_AMBIGUOUS`.

TLS matching uses the existing protocol-owned explicit SNI/ALPN keys. Trojan and AnyTLS require SNI unless default; Reality uses SNI or destination-derived servername. TrustTunnel without explicit SNI matches h2 at any SNI; with explicit SNI, it matches exactly. An absent-SNI key otherwise matches only absent SNI. Explicit SNI is recommended for certificate-TLS members. A sole TLS server may be default; a shared server without an explicit usable key must itself be default. A no-SNI default link may use host-derived SNI and fall back. A default handles valid unmatched TLS only, never overlaps. Missing-key hint: `set sni on {NAME} or mark it as the default`; overlapping keys require changing SNI. Trial timestamps are 28 seconds for Shadowsocks 2022 and 118 seconds for VMess. Bounded replay filters are per endpoint, with TTLs of 60 and 240 seconds respectively. Buffer limit is 64 KiB when TLS is eligible, otherwise 4 KiB; deadline is 3 seconds. Trial rejection silently drains without writing or closing first, until peer close or randomized 30–90 second idle. Concurrent drains and bytes are bounded; resource caps may close. Pending trials are capped per endpoint and source. When a local angel is available, `status` exposes `slow_first_byte`, `replayed` and `ambiguous` counters; zero values are omitted. `list`/`show` include live counters when available, otherwise configuration only. `doctor` reports safe aggregate counter summaries. UDP/QUIC sharing (stage 3) is not implemented.

## Which exit a client gets

After authentication, each client uses the route configured on its account; the server itself has no route override. An account in `route` mode uses its configured provider/country nodes and fails closed if none match. An account with `exit: direct` leaves from the server machine directly (optionally through its effective post proxy); it does not use a VPN node. Direct exit exposes that machine's address to destinations, so treat it as an explicit security-sensitive choice. See [account direct exits](../design/exit-direct.md).

## Accounts, filtering, and lifecycle

Server traffic follows the configured account route, including direct/post-hop behavior for accounts without a route. Route-to-direct and direct-to-route changes apply in place with open connections retained only when at least one account route has stored nodes both before and after. The first node-backed route appearing or last disappearing, direct-only changes, and changes after the last node-backed route restart the listener. Keeping another account's node-backed route through a flip preserves connections. Adding an account always restarts, as with `account add`. `listen.allowed_clients` is enforced together with an account's optional `allowed_clients`: the client must pass both rules. An absent account rule imposes no extra restriction. Account allowlist-only changes apply hot to server policy without listener restart; existing connections survive, including mixed authentication and server endpoints. Global `listen.allowed_clients`, credentials, account membership, bind, port, and other server changes require listener restart/reconfiguration.

Generated passwords, UUIDs, keys, private keys and other material are stored in `{HOME}/server-secrets.ktav` with owner-only permissions, separate from `config.ktav`. Configuration and secret displays redact parameter values and credentials. A generated URI or Mihomo snippet intentionally contains credentials; protect its output. `server rotate` replaces user credentials or, without `--account`, server material and all user secrets.

A loopback server may bind `127.0.0.1` but advertise `::ffff:127.0.0.1` in `public_host`/`--host` when creating a local share link. The subscription parser drops banners advertising `127.0.0.1`, `0.0.0.0`, `::1`, or `localhost`; the mapped IPv4 address remains local. The server listener must still bind to `127.0.0.1`.

QUIC listeners bind UDP sockets. A changed UDP-bound listener closes before its replacement binds, briefly interrupting service and ending existing sessions; socket release is synchronous. Failed replacement restores the previous UDP listener best-effort. UDP relay payloads are dropped and counted, even on UDP-bound protocols. The conflict check is per transport: a TCP server and a UDP-bound server may share a port number, and a UDP-bound server may use the proxy port; two servers of the same transport on overlapping binds are refused with `PORT_IN_USE`, except eligible shared TCP listeners.

## Testing

Unit tests validate configuration, links, and filtering without opening sockets. Integration tests use the embedded core with a matching Mihomo client connected over loopback. E2e tests run two real angels using a generated link or subscription; these run only in CI. Mieru and shadowquic have no matching e2e test files. See [testing strategy](../testing.md).
