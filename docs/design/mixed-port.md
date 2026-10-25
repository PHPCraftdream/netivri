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
- Mihomo's remote-address filtering (`lan-allowed-ips`) now DOES apply to our
  store. netivri enables it in the accept loop: the global
  `listen.allowed_clients` rule is checked against the peer address of every
  accepted connection and a refused connection is closed before any handshake
  (no reply bytes). The filter is set after `executor.ApplyConfig` (which resets
  the process-global state on every call) and reset to allow-all when the
  instance closes. It is enabled with a tiny vendored patch (option (a))
  because `mixed.NewWithConfig` offers no listener or conn-wrapper injection
  point, so the filter had to be enabled inside the accept loop; `skip-auth`
  stays default-listener-only, so authentication is never skipped. The default
  bind is `127.0.0.1`; a non-loopback bind requires auth.

### Client address rules

Enforcement order for `listen.allowed_clients` and the per-account
`allowed_clients`:

1. The global rule, at accept time, before any SOCKS or HTTP handshake: the
   peer address is matched against the rule and a refused connection is closed
   with no reply bytes.
2. The per-account rule, during authentication, through the vendored `VerifyFrom`
   seam (SOCKS5 user/pass and HTTP `Proxy-Authorization` pass the peer address
   to the authenticator): it is checked before the password is verified, so an
   `init` placeholder can never be claimed from a disallowed address.

The rules cascade: an account rule can only narrow the global rule, never
widen it (see [docs/config.md](../config.md), "Allowed clients"). A
per-account refusal during authentication is indistinguishable from a
wrong-password failure: the peer is answered 403 when credentials were
sent, and 407 only when none were.

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
