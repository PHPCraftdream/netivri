# Mixed port: SOCKS5 and HTTP on one port

Verified by reading `github.com/metacubex/mihomo` v1.19.32 (`listener/mixed`,
`listener/socks`, `listener/http`, `component/auth`, `hub/executor`).

## Protocol detection

`listener/mixed.handleConn` peeks the first byte of each connection:

| First byte | Handler |
|---|---|
| `0x04` | SOCKS4 / SOCKS4a |
| `0x05` | SOCKS5 |
| anything else | HTTP proxy (plain proxy requests and `CONNECT`) |

The protocol is chosen by how the client starts the dialog; nothing is
configured. A garbage first byte goes to the HTTP parser and the connection is
closed.

## Authentication

`component/auth.Authenticator` is `Verify(user, pass string) bool` plus
`Users() []string`. It is called:

- SOCKS5: once per connection (RFC 1929 handshake).
- HTTP: for every request (`Proxy-Authorization: Basic`, `407` on failure),
  including every request on a keep-alive connection. `Verify` is a hot path.
- SOCKS4: only a user id exists, so `pass` is empty. netivri treats an empty
  password as invalid, so SOCKS4 is rejected when auth is enabled.

`listener/auth.Default` is a process-global store that
`executor.ApplyConfig` overwrites on every call (`updateUsers`, from the
config's `authentication` list). netivri must not rely on it.

## Wiring (option 1: library, with our own listener)

- `executor.ApplyConfig` loads proxies, rules and DNS. In the Mihomo config
  every listener port is `0` (including `mixed-port`), so the executor opens
  no listeners.
- netivri opens its own listener:

  ```go
  mixed.NewWithConfig(
      LC.AuthServer{Enable: true, Listen: addr, AuthStore: authStore.NewAuthStore(ourAuth)},
      inbound.NewListenConfig(),
      tunnel.Tunnel,
      inbound.WithInName("NETIVRI-MIXED"), inbound.WithSpecialRules(""),
  )
  ```

  Our own `AuthStore` is untouched by later `ApplyConfig` calls. Explicit
  `additions` are required: with none, the listener counts as the default one
  and Mihomo's global allow/skip-auth lists apply.
- Port busy: `Listen` returns an error, mapped to `PORT_IN_USE` (exit 6).
- Mihomo's remote-address filtering (`lan-allowed-ips`) does not apply to our
  store. The default bind is `127.0.0.1`; a non-loopback bind requires auth.

## UDP

For `UDP ASSOCIATE` the SOCKS5 TCP handler only holds the control connection
open and discards data, so the relay must come from a separate UDP listener
that `listener/mixed` does not start. v1 is TCP only. Revisit when UDP is
scheduled.

## To check in the CI integration test (#7)

- SOCKS5 `CONNECT`, HTTP plain proxy `GET` and HTTP `CONNECT` work on the same
  port; a garbage first byte is rejected.
- With auth enabled: SOCKS4 is rejected, an empty password never succeeds
  (including in `init` mode), wrong credentials fail on both protocols.
- HTTP keep-alive: credentials are checked on every request without re-hashing.
