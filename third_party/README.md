# Vendored third-party sources

## mihomo

- Upstream: <https://github.com/MetaCubeX/mihomo>, module
  `github.com/metacubex/mihomo`, version `v1.19.32`.
- Wired in by `replace github.com/metacubex/mihomo => ./third_party/mihomo`
  in `go.mod`; the `require` line keeps the upstream version.
- License: GPL-3.0 (`third_party/mihomo/LICENSE`), the same as netivri.
- Why vendored: netivri needs changes inside Mihomo's outbound adapters that
  the public API does not offer (see `docs/design/latency.md`).

### Rules

- The import commit is pristine: the tree equals the module `v1.19.32` as
  published (`diff -r` against the Go module cache is empty). It is stored
  byte for byte (`.gitattributes` disables line-ending conversion).
- Every change to an upstream file is its own commit, listed under "Patches"
  below with its reason. New code goes into new files named `netivri_*.go`
  in the same package; edits to upstream files stay minimal.
- Each modified upstream file carries a short notice at the top, as GPL-3.0
  section 5(a) asks: `// Modified by the netivri authors: <what and why>.`
- `gofmt` is not run on this directory and Mihomo's own tests are not run.

### Updating to a new upstream version

1. Import the new tag pristine over the directory in one commit.
2. Re-apply the patch commits listed below (cherry-pick, resolve conflicts).
3. Change the version here and in `go.mod`, run `go mod tidy`, then run the
   full CI and the release dry run.

### Patches

1. **VLESS transport seam** (`adapter/outbound/netivri_transport.go` plus
   minimal edits to `adapter/outbound/vless.go`): adds `PrepareTransport`,
   `TransportSupported`, `SetTransportSource`, `SetPhaseObserver` and
   `UnwrapVless` to the `outbound` package so netivri
   can pre-dial the TCP + TLS/Reality transport and measure the `tcp` and
   `tls` phases of a dial (`docs/design/latency.md`, "VLESS seam"). Without a
   source and an observer, behaviour is unchanged from upstream.
2. **Allowed-client address gate** (`listener/mixed/mixed.go`): the accept
   loop applies Mihomo's remote-address filter (`inbound.IsRemoteAddrDisAllowed`)
   to listeners with a custom auth store too, so netivri's
   `listen.allowed_clients` rule closes a refused connection before any
   SOCKS/HTTP handshake. Skip-auth (`SkipAuthRemoteAddr`) stays
   default-listener-only, so proxy authentication is never skipped. The
   filter is process-global state; netivri sets it after `executor.ApplyConfig`
   and resets it to allow-all when an instance closes.
3. **VerifyFrom authentication seam** (`component/auth/netivri_remote.go`,
   `transport/socks5/socks5.go`, `listener/http/proxy.go`): adds the optional
   `auth.RemoteVerifier` interface. When netivri's authenticator implements
   `VerifyFrom(user, pass, remote)`, the SOCKS5 user/pass handshake and the
   HTTP Proxy-Authorization check call it instead of `Verify`, so a
   per-account client-address rule is enforced before the password is
   verified and before an `init` account can be claimed. Without the
   interface the behaviour is unchanged from upstream. A failed
   re-authentication on a keep-alive HTTP connection revokes trust and clears
   the recorded user, so a request naming an account whose rule rejects the
   peer is answered exactly like a wrong password (403; 407 only when no
   credentials were sent) and never routed.
4. **Atomic address filter lists** (`adapter/inbound/ipfilter.go`): the
   allowed and disallowed prefix lists are `atomic.Pointer` values instead of
   plain slices. The accept loops read them while netivri sets them when an
   instance starts and resets them when it closes; upstream never changes
   them after start, so it had no synchronization (the race detector found
   the race in `TestMixedPortLifecycle`). Behaviour is otherwise unchanged.
5. **Locked proxy map read** (`tunnel/tunnel.go`): `Proxies()` (the function
   and the `tunnel` method that `proxydialer` calls to resolve a
   `dialer-proxy` by name) takes `configMux.RLock` like the other readers.
   netivri replaces the proxy map in a running instance (`Reconfigure`,
   `UpdateProxies`) while dials look their `dialer-proxy` up, which is the
   SOCKS5 chain's pre and post hop; upstream never replaced it while
   serving. The race detector found it in `TestSOCKS5PreReconfigureKeepsOpenConnection`.
6. **Atomic Vision state** (`transport/vless/vision/conn.go`,
   `transport/vless/vision/netivri_atomic.go`): `readLastCommand` and
   `writeDirect` are `atomic` values. `Upstream()` runs on the relay goroutine
   (it picks the MTU of the reader) while `ReadBuffer` and `WriteBuffer` update
   the two fields on the connection's own read and write goroutines; upstream
   has no synchronization there. The race detector found it on macOS in
   `TestVlessRealityServerRoutesAndRejectsInvalidCredentials`. Behaviour is
   otherwise unchanged.
