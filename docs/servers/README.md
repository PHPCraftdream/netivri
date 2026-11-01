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

Shared TCP ports support exactly four protocols: [trojan](trojan.md), [anytls](anytls.md), [trusttunnel](trusttunnel.md), and [vless-reality](vless-reality.md). Plain VLESS and VMess are not supported for shared-port classification. Shared members need protocol-declared explicit match keys matching their exported links: Trojan and AnyTLS require `sni` unless the server is the default; VLESS-Reality uses `sni` or a destination-derived servername. TrustTunnel without explicit `sni` matches h2 at any SNI; with explicit `sni`, it matches exactly. An absent-SNI key otherwise matches only absent SNI. Explicit `sni` is recommended for certificate-TLS members. A sole TLS server may be the default; a shared server without an explicit usable key must itself be the default. A no-`sni` default link may have host-derived SNI that matches no key and falls back. `SERVER_PORT_AMBIGUOUS` for a missing explicit key hints `set sni on {NAME} or mark it as the default`; for overlapping keys, change `sni` (a default cannot resolve overlap). A default accepts unmatched valid TLS only.

## Which exit a client gets

After authentication, each client uses the route configured on its account; the server itself has no route override. An account in `route` mode uses its configured provider/country nodes and fails closed if none match. An account with `exit: direct` leaves from the server machine directly (optionally through its effective post proxy); it does not use a VPN node. Direct exit exposes that machine's address to destinations, so treat it as an explicit security-sensitive choice. See [account direct exits](../design/exit-direct.md).

## Accounts, filtering, and lifecycle

Server traffic follows the configured account route, including direct/post-hop behavior for accounts without a route. Route-to-direct and direct-to-route changes apply in place with open connections retained only when at least one account route has stored nodes both before and after. The first node-backed route appearing or last disappearing, direct-only changes, and changes after the last node-backed route restart the listener. Keeping another account's node-backed route through a flip preserves connections. Adding an account always restarts, as with `account add`. `listen.allowed_clients` is enforced together with an account's optional `allowed_clients`: the client must pass both rules. An absent account rule imposes no extra restriction. Account allowlist-only changes apply hot to server policy without listener restart; existing connections survive, including mixed authentication and server endpoints. Global `listen.allowed_clients`, credentials, account membership, bind, port, and other server changes require listener restart/reconfiguration.

Generated passwords, UUIDs, keys, private keys and other material are stored in `{HOME}/server-secrets.ktav` with owner-only permissions, separate from `config.ktav`. Configuration and secret displays redact parameter values and credentials. A generated URI or Mihomo snippet intentionally contains credentials; protect its output. `server rotate` replaces user credentials or, without `--account`, server material and all user secrets.

A loopback server may bind `127.0.0.1` but advertise `::ffff:127.0.0.1` in `public_host`/`--host` when creating a local share link. The subscription parser drops banners advertising `127.0.0.1`, `0.0.0.0`, `::1`, or `localhost`; the mapped IPv4 address remains local. The server listener must still bind to `127.0.0.1`.

QUIC listeners bind UDP sockets. A changed UDP-bound listener closes before its replacement binds, briefly interrupting service and ending existing sessions; socket release is synchronous. Failed replacement restores the previous UDP listener best-effort. UDP relay payloads are dropped and counted, even on UDP-bound protocols. The conflict check is per transport: a TCP server and a UDP-bound server may share a port number (for example Trojan on TCP 443 and Hysteria2 on UDP 443), and a UDP-bound server may use the proxy port; two servers of the same transport on overlapping binds are refused with `PORT_IN_USE`, except TLS-sniffable TCP listeners may share when their ClientHello matches uniquely.

## Shared TCP ports: TLS stage 1

TLS-capable protocol listeners may share a bind and TCP port only when each implements the TLS-family sniffer (Trojan, AnyTLS, TrustTunnel, and VLESS Reality). TCP and UDP remain separate transports and may use the same port number; UDP/QUIC sharing is not implemented.

The sniffer peeks without consuming ClientHello bytes, then replays them to the selected listener. It reads at most 64 KiB and waits at most 3 seconds for the first byte/ClientHello. Peeked bytes are never logged. A slow or incomplete first byte is closed and counted. `unclassified` counts unmatched or malformed ClientHellos; `slow_first_byte` counts connections that exceed the first-byte deadline. Status server entries expose sorted endpoint `members`, the member's `default` boolean, and these counters.

Matching uses configured SNI and ALPN allowed sets. SNI comparison is exact and case-insensitive; ALPN comparison is exact and case-sensitive. An absent-SNI key matches only absent SNI; a TrustTunnel server without an explicit `sni` matches any SNI through its h2 ALPN key. An IP literal is never sent as SNI, so it does not count as an explicit key. A client offering ALPN values from both servers' sets fails closed rather than selecting arbitrarily. A default handles only a valid TLS ClientHello with no matching server; it never resolves overlapping keys. Choose another SNI or ALPN to avoid ambiguity.

`default: true` (or `server add --default`) selects the single TLS fallback only for a valid TLS ClientHello that matches no configured SNI/ALPN set. It does not accept malformed or non-TLS traffic. Overlapping ALPN sets for the same SNI are ambiguous and refused even if one server is default. Such conflicts return `SERVER_PORT_AMBIGUOUS` (exit 6), with a hint to change SNI/ALPN; a default cannot resolve ambiguity. Non-sniffable same-transport conflicts remain `PORT_IN_USE`.

Only TLS-family classification is implemented. Cleartext trial authentication (stage 2) and UDP/QUIC ALPN multiplexing (stage 3) are not implemented.

## Testing

Unit tests validate configuration, links, and filtering without opening sockets. Integration tests use the embedded core with a matching Mihomo client connected over loopback. E2e tests run two real angels using a generated link or subscription; these run only in CI. Mieru and shadowquic have no matching e2e test files. See [testing strategy](../testing.md).
