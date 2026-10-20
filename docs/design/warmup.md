# Warm connections: keeping upstreams hot (design notes)

> Research notes against Mihomo v1.19.32; line references are to that version,
> re-verify the exact code before building on a claim.

Task #14 (rev. 4, final). Research of Mihomo **v1.19.32**
(`github.com/metacubex/mihomo@v1.19.32`), read-only, no code. References are
`file:function` (with line numbers where stable) inside that module source.
Every claim below was read from the source unless marked **unknown** or
**unverified**.

Goal: decide how netivri keeps upstream connections warm so clients see a fast
time-to-first-byte (TTFB), and which mechanisms work when Mihomo is used as a
library **without forking it**.

---

## 1. The dial path (what a new client connection pays today)

`tunnel.handleTCPConn` resolves a proxy from the rule/group and calls
`proxy.DialContext(ctx, dialMetadata)` per client connection, wrapped in
`retry` (`tunnel/tunnel.go:574-577`, `tunnel.handleTCPConn`).
The interface is `C.ProxyAdapter` (`constant/adapters.go:126-152`,
`constant.ProxyAdapter`): strictly "give me metadata, receive a connected
tunnel". No method of the interface accepts a caller-supplied connection.

For the classic stream protocols the per-request sequence is:

1. raw TCP dial through the adapter's dialer: `Vless.dialContext`
   (`adapter/outbound/vless.go:340`), `Trojan.dialContext`
   (`adapter/outbound/trojan.go:210`), `Vmess.dialContext`
   (`adapter/outbound/vmess.go:382`);
2. TLS / REALITY / shadowtls / restls handshake, done **inside** the protocol
   setup for the default `tcp` network (`Vless.StreamConnContext` default case
   → `Vless.streamTLSConn`, `adapter/outbound/vless.go:258-262` and
   `vless.go:306`);
3. protocol header with the **destination**, written in
   `Vless.streamConnContext` → `parseVlessAddr` (`vless.go:271`, `vless.go:434`),
   `Trojan.writeHeaderContext` (`trojan.go:197`).

Every client connection therefore pays: TCP + TLS(+REALITY) + protocol header,
unless a multiplexed/pooled transport is configured (section 2).

### 1.1 Is the transport/protocol split exposed? (three separate questions)

**(a) Can external code inject a raw TCP connection?** **Yes — via the dialer,
not the adapter.** `BasicOption.DialerForAPI` (`adapter/outbound/base.go:204`,
field doc: "the dialer used for API usage has higher priority than all the
above configurations") is consumed by `BasicOption.NewDialer`
(`base.go:209-219`) and is set per proxy through `adapter.WithDialerForAPI`
(`adapter/parser.go:268-272`). Every adapter builds its dialer this way
(e.g. `Tuic` at `adapter/outbound/tuic.go:258`, `AnyTLS` at
`adapter/outbound/anytls.go:111`). A `C.Dialer` is a thin interface
(`constant.Dialer`, `constant/adapters.go:110-113`: `DialContext` +
`ListenPacket`; the stock implementation is `dialer.Dialer.DialContext`,
`component/dialer/dialer.go:423`), so netivri can supply
a **pooling dialer** that returns pre-established raw TCP connections.
Caveats (all observed in source):
- `DialerForAPI` **replaces** the option-derived dialer entirely
  (`base.go:210-216`), so a pooling dialer must itself apply
  `DialOptions()` (interface, routing-mark, prefer, tfo, mptcp —
  `Base.DialOptions`, `base.go:142-170`) and must target the same
  `server:port` string the adapter dials (e.g. `v.addr`, `vless.go:348`).
- A pool only saves the TCP handshake (~1 RTT). TLS and the protocol header
  are still paid per client connection (they live in steps 2-3 above).
- Pooled idle TCP connections can be dropped by the server/NAT; liveness
  handling is netivri's own problem (nothing in mihomo helps here).

**(b) Can external code pre-establish TCP+TLS (incl. REALITY)?** **No, not
without a fork.** For the default `tcp` network the TLS wrap is unconditional
inside `StreamConnContext` (`vless.go:258-262` → `streamTLSConn`,
`vless.go:306-338`, via `vmess.StreamTLSConn` with the full TLSConfig incl.
`Reality` at `vless.go:310-324`). Handing in an already-TLS'd conn would wrap
TLS a second time; there is no exported "wrap transport only" entry point.
`vmess.StreamTLSConn` itself is exported (`transport/vmess`), but the adapter
gives no way to make `StreamConnContext` *skip* its own TLS step.

**(c) Full tunnels: two separate questions.**

- *Fixed-destination parking:* a wrapper adapter's `DialContext`
  (`constant/adapters.go:126-151` — nothing in the interface forbids returning
  a previously established `C.Conn`) may keep a completed tunnel keyed by its
  destination and hand it out again for a repeated dial to the same
  destination. The destination is written once in the protocol header
  (`Vless.streamConnContext` → `parseVlessAddr`, `vless.go:271,434-459`;
  `Trojan.writeHeaderContext`, `trojan.go:197`) — so the parked tunnel can
  only ever serve that one destination. No fork needed; see the verdict and
  lifecycle requirements in §5/#16(1).
- *Retargeting a committed tunnel:* **impossible, with or without a fork.**
  The destination bytes are already on the wire to the server; no local code
  change can redirect an established tunnel to a different destination. A
  fork would not help; only a *new* dial gets a new destination.

Wrapping an adapter (intercepting `DialContext`) is a supported pattern
(`SingMux`, `SingMux.DialContext` `adapter/outbound/singmux.go` at
`singmux.go:50`, wired at `adapter/parser.go:234-246`). Two wrapper
capabilities must not be conflated:

- a wrapper **CAN return its own parked `C.Conn`** from its `DialContext`
  (e.g. from a destination-keyed pool) — the interface
  (`constant/adapters.go:126-151`) imposes no restriction on where the
  returned connection comes from; see §5/#16(1);
- a wrapper **CANNOT inject a transport into an existing adapter's
  internals**: the inner adapter still runs its own dial + TLS + protocol
  steps (`Vless.DialContext`, `vless.go:352-365`), so pre-establishing those
  steps requires the **dialer** hook (point a) or a Mihomo fork (point b).

## 2. What already keeps state across client connections (no fork needed)

Most interesting transports keep expensive state in a long-lived client object
created once at config-parse time and reused for every dial. For these,
"keep warm" = "keep the process and client object alive", plus optional
periodic probing.

### 2.1 Main protocols

| Transport / protocol | Cold cost (new underlying connection) | Reusable warm state | Evidence |
|---|---|---|---|
| vless/vmess/trojan, `network: tcp` (+tls/reality/vision) | TCP + TLS/REALITY + header; vision (`xtls-rprx-vision`, `transport/vless/vless.go:15`; flow truncated to 16 chars, `vless.go:462-466`) adds padding | none in adapter; raw TCP pool possible via dialer hook (§1.1a) | `vless.go:340,352,258-262,306` |
| vless/vmess/trojan, `network: grpc` (gun) | first dial: TCP + TLS + h2 | `gun.Client` pools h2 transports; later dials reuse them; h2 ping keep-alive via `ping-interval` | `transport/gun/gun.go:398` (`NewClient`), `gun.go:410` (`Dial`), `gun.go:427` (`getTransport`); ping wired at `gun.go:310` (`HTTP2Config.SendPingTimeout`); options `GrpcOptions` `adapter/outbound/vmess.go:156-163` |
| vless `network: xhttp` | per XMUX policy | `xhttp.Client` reuse (XMUX): `max-concurrency`, `h-keep-alive-period`, … (runtime idle semantics **unverified**, lives in `transport/xhttp`) | `XHTTPReuseSettings` `vless.go:131-139`; client created `vless.go:911` (`NewVless`) |
| vless/vmess `network: ws` / `h2` | per dial (TLS + upgrade / TLS + h2) | none | `Vless.StreamConnContext` ws/h2 cases (`vless.go:155ff`, h2 at `vless.go:242-253`) |
| sing-mux (`smux`) | smux/yamux/h2mux protocol, needs **server support** | `mux.Client` keeps pooled sessions; `offer` reuses a session that `CanTakeNewRequest` | `SingMux` `adapter/outbound/singmux.go` (`SingMuxOption` at `singmux.go:23`, `NewSingMux` at `singmux.go:94`); pooling `sing-mux/client.go:29` (`connections` list) and `client.go:150` (`offer`) |
| hysteria2 | first dial: QUIC handshake + auth | sing-quic `Client` caches one `clientQUICConnection` and reuses it while `active()`; later dials open cheap streams | `Hysteria2.DialContext` `adapter/outbound/hysteria2.go:92`; reuse in `sing-quic/hysteria2/client.go:206-222`; keep-alive: **unknown** (see §2.3) |
| tuic | first dial: QUIC handshake + auth; `reduce-rtt` = 0-RTT **early dial** option (`Tuic.dial`, `tuic.go:113-114`) — note: this is not a keep-alive knob | `tuic.PoolClient` pools QUIC conns (v4/v5); QUIC keep-alive configurable via `heartbeat-interval` → `quic.Config.KeepAlivePeriod` | `Tuic.DialContext` `tuic.go:73`; `TuicOption.HeartbeatInterval` `tuic.go:43`; `quicConfig` `tuic.go:199-210` (KeepAlivePeriod at `:206`); pools at `tuic.go:282,296` (`NewPoolClientV4/V5`) |
| anytls | cold session = TCP + TLS + password auth (`anytls.Client.createOutboundTLSConnection`, `transport/anytls/client.go:68-99`) | `anytls.Client` idle-session pool: `idle-session-check-interval`, `idle-session-timeout`, `min-idle-session`, `disable-reuse`; per-request cost is a stream open + dest write | options `AnyTLSOption` (`adapter/outbound/anytls.go:34-43` area), wiring `anytls.go:114-123`, client `anytls.go:172` (`NewAnyTLS`); pool `transport/anytls/client.go:41-53` (`NewClient`), stream open `client.go:55-66` (`CreateProxy`) |
| shadowsocks (+obfs/plugins) | TCP + AEAD (+obfs/plugin handshake) per dial | **kcptun plugin**: persistent client, dials are `OpenStream` on it (`ShadowSocks.dialContext`, `adapter/outbound/shadowsocks.go:214-218`). Plain tcp/v2ray-plugin/simple-obfs: none | `shadowsocks.go:153-212` (`StreamConnContext`), `shadowsocks.go:222` (`DialContext`) |
| shadowsocksr | TCP + SSR protocol + obfs + param auth per dial | none | `ShadowSocksR.DialContext` `adapter/outbound/shadowsocksr.go:68-79` |

### 2.2 Long tail: full outbound inventory (`adapter/outbound`)

Grouped; "cold" = cost when the adapter must establish a new underlying
connection; "reuse" = what survives across client connections.

| Adapter | Cold cost | Reuse | Evidence |
|---|---|---|---|
| http | TCP (+TLS if `tls`) + CONNECT per dial | none | `Http.DialContext` `http.go:63` |
| socks5 | TCP (+TLS if `tls`) + SOCKS5 handshake/auth per dial | none | `Socks5.DialContext` `socks5.go:71` |
| ssh | TCP + SSH handshake + auth (first time); then channel open per dial | one cached `ssh.Client` (reconnects only after close) | `Ssh.DialContext` `ssh.go:44-50`; cache in `Ssh.connect` `ssh.go:57-63` |
| snell | TCP + obfs/tls(+jls) + snell protocol per dial (only when reuse is off) | **connection pool**: `reuse: true` (default on for v2, opt-in for v4 — `NewSnell` `snell.go:252`); `Snell.DialContext` gets a pooled conn and marks it reusable (`snell.go:102-116`); pool built in `NewSnell` (`snell.go:287-300`); closed-but-reusable conns return to the pool (`transport/snell/pool.go:98-118`, `PoolConn.Close`); pool defaults: age 15 s, size 10 (`snell.NewPool`, `pool.go:123-135`) | `snell.go:102,167,252,287-300`; `transport/snell/pool.go` |
| hysteria (v1) | QUIC handshake + auth (first time) | persistent `core.Client` (QUIC session) | `Hysteria.client` `hysteria.go:47`; `core.NewClient` `hysteria.go:236` |
| mieru | transport handshake on first use (client started lazily) | persistent `mieruclient.Client` | `mieru.go:26`; `NewMieru`/lazy start `mieru.go:140-149` |
| masque (CONNECT-IP) | mode-dependent: QUIC+h3 session (`h3`/`h3-l4proxy`), or HTTP/2 over TCP+TLS (`network: h2` — h2 transport built in `NewMasque`, `masque.go:180-207`) | QUIC path: persistent `masque.L4Client`, but **only** in `h3-l4proxy` mode (`NewMasque` `masque.go:158,223-231`); h2 path reuses the `http.Transport` h2 pool | `NewMasque` `masque.go:109`; field `masque.go:42` |
| shadowquic | QUIC + protocol handshake | persistent `shadowquic.Client` | `shadowquic.go:23`; `shadowquic.NewClient` `shadowquic.go:176` |
| trusttunnel | TCP+TLS + protocol auth, or QUIC when `quic: true` (`TrustTunnelOption.Quic` `trusttunnel.go:39`; wired into `ClientOptions` in `NewTrustTunnel` `trusttunnel.go:106-119`) | `trusttunnel.PoolClient` (pooled; `max-connections`/`min-streams`/`max-streams` knobs, `trusttunnel.go:44-46,117-119`) | `TrustTunnel.DialContext` `trusttunnel.go:49`; `NewPoolClient` `trusttunnel.go:141` |
| sudoku | TCP + sudoku obfs (+httpmask tunnel when configured; tunnel client cached per config key) | cached `httpmask.TunnelClient` (httpmask mode only) | `getOrCreateHTTPMaskClient` `sudoku.go:365-399` |
| gost relay | TCP (+TLS/ws) + relay handshake per dial | none observed | `GostRelay.DialContext` `gost_relay.go:39` |
| wireguard | L3 overlay: persistent WireGuard tunnel established at adapter construction; per-client-conn cost is userspace routing (internals **not read line-by-line**) | the tunnel itself | `NewWireGuard` `wireguard.go:351` (`wireguard.go:351-360`) |
| openvpn | L3 overlay: persistent OpenVPN client (`ovpn.Client`) | the tunnel | field `OpenVPN.client` `openvpn.go:30`; `ovpn.NewClient` inside `OpenVPN.startLocked` (`openvpn.go:316`, call at `openvpn.go:321`), started lazily via `OpenVPN.run` (`openvpn.go:257`) |
| easytier | mesh overlay, persistent core host instance | the mesh session | fields `EasyTier.host`/`EasyTier.instance` `easytier.go:51-52` (`corehost.Host`/`corehost.Instance`); `NewEasyTier` `easytier.go:128`; `EasyTier.DialContext` `easytier.go:432` (internals **not read line-by-line**) |
| tailscale | mesh overlay; `Tailscale.start` brings up the tsnet/backend (`tailscale.go:186`), dials wait for backend init (`ensureStarted` `tailscale.go:206`) | the tsnet backend session | `NewTailscale` `tailscale.go:114`; `Tailscale.DialContext` `tailscale.go:349` (internals **not read line-by-line**) |
| zerotier | mesh overlay, persistent runtime with background tasks (`zeroTierRuntime.startBackgroundTasks` `zerotier.go:421`; `ZeroTier.startLocked` `zerotier.go:454`) | the network/runtime session | `NewZeroTier` `zerotier.go:201`; `ZeroTier.DialContext` `zerotier.go:1253` (internals **not read line-by-line**) |
| jls / restls / shadowtls / tlsmirror / reality | not standalone outbounds: TLS-layer plugins/options consumed by other adapters (e.g. `RealityOptions` in `vless.go:63`, reality/tlsmirror handled in gun's ALPN check `gun.go:282`) | — | `reality.go`, `restls.go`, `shadowtls.go`, `tlsmirror.go` |
| direct | plain TCP dial to the destination — no proxy handshake, but real network setup cost | none | `Direct.DialContext` `direct.go:24-34` (`direct.go:30` dials `metadata.RemoteAddress()`) |
| reject | control adapters (`REJECT`/`REJECT-DROP`/`PASS`): return a nop/drop conn, no upstream connection | — | `Reject.DialContext` `reject.go:25`; constructors `NewRejectWithOption`/`NewRejectDrop`/`NewPass` `reject.go:47-91` |
| dns | DNS hijack: returns an in-process pipe to the resolver (`N.Pipe`), no upstream connection | — | `Dns.DialContext` `dns.go:26-30` |
| rematch / util | rematch marker adapter (re-runs rule matching, `Rematch.DialContext` `rematch.go:23`) and shared helpers (`util.go`) | — | `rematch.go:23`, `util.go` |

### 2.3 Keep-alive facts and unknowns

- gun: h2 ping via `ping-interval` → `HTTP2Config.SendPingTimeout`
  (`gun.go:310`; zero disables the health check).
- tuic: `heartbeat-interval` → `quic.Config.KeepAlivePeriod` (`tuic.go:43,206`).
- anytls: idle-session pool knobs (`anytls.go:114-123`).
- hysteria2: keep-alive setting **unknown** — `quicConfig` in
  `NewHysteria2` (`hysteria2.go:201-206`) sets only receive windows; whether
  sing-quic sets `KeepAlivePeriod` internally was not traced.
- sing-mux: no keep-alive knob surfaced by mihomo; only gun's `ping-interval`
  was found as an explicit liveness knob for TCP-mux transports.
- ssh client and hysteria-v1 session: no re-keep-alive mechanism found; if the
  cached client dies, the next dial reconnects from scratch
  (`ssh.go:57-63` has no health probing).

### 2.4 Feasibility table (mechanism × protocol class)

Legend: **works** = no fork, no server change; **server** = needs server
support; **fork** = needs patched Mihomo; **n/a**.

| Mechanism | tcp-tls stream protocols | grpc/gun | xhttp | sing-mux | hysteria2 | tuic | anytls | ss (plain) |
|---|---|---|---|---|---|---|---|---|
| Persistent client object (intrinsic warm state) | n/a | works | works | works+server | works | works | works | n/a (kcptun plugin: works) |
| Keep-alive tuning via node options | n/a | works (`ping-interval`) | partial (`reuse-settings`, runtime unverified) | not found | unknown | works (`heartbeat-interval`); `reduce-rtt` = early dial | works (`idle-session-*`) | n/a |
| Raw TCP pre-dial pool via dialer hook (§1.1a) | works | not needed | not needed | not needed | not needed (UDP) | not needed (UDP) | not needed | works |
| TCP+TLS/REALITY pre-establishment | **fork** | not needed (already pooled) | not needed | not needed | not needed | not needed | not needed | fork (n/a: no TLS) |
| Full-tunnel parking, fixed destination only (§5/#16(1)) | works (wrapper adapter; lifecycle is netivri's job) | not needed | not needed | not needed | not needed | not needed | not needed | works |

Retargeting a committed tunnel to a different destination: **impossible,
with or without a fork** (destination bytes already sent to the server;
§1.1c).

### 2.5 Node fields netivri can expose (knobs)

From `BasicOption` (`adapter/outbound/base.go:193-199`) and per-protocol
options:

- `tfo`, `mptcp` — `base.go:194-195`; applied by `Base.DialOptions`
  (`base.go:142-170`).
- `interface-name`, `routing-mark`, `ip-version`, `dialer-proxy` —
  `base.go:196-199`.
- `udp`, `packet-addr`, `xudp`, `packet-encoding` (vless) — `VlessOption` fields `vless.go:67-70`.
- global: `tcp-concurrent`, `keep-alive-idle`, `keep-alive-interval`,
  `disable-keep-alive` — `config/config.go:442,447-449`.
- grpc: `ping-interval`, `max-connections`, `min-streams`, `max-streams` —
  `GrpcOptions` `vmess.go:156-163`.
- xhttp: `reuse-settings` (XMUX), incl. `h-keep-alive-period` —
  `vless.go:131-139`.
- sing-mux: `smux: {enabled, protocol, max-connections, min-streams,
  max-streams, padding, only-tcp, brutal-opts}` — `SingMuxOption`
  `singmux.go:23-33`, wired at `adapter/parser.go:234-246`.
- anytls: `idle-session-check-interval`, `idle-session-timeout`,
  `min-idle-session`, `disable-reuse` — `anytls.go:114-123`.
- tuic: `heartbeat-interval` (`tuic.go:43`), `reduce-rtt` (`tuic.go:45`),
  `congestion-controller`, `cwnd`, `bbr-profile` (`tuic.go:48,54-55`).

## 3. Groups and health checks (what a probe actually does)

- `provider.HealthCheck` ticks every `interval` (`HealthCheck.process`,
  `adapter/provider/healthcheck.go:44-62`); with `lazy` it **skips** a tick
  when the group was not used within one interval (`healthcheck.go:50-56`).
  Each run calls `p.URLTest(...)` per proxy with concurrency limit 10
  (`HealthCheck.check`/`execute`). Default timeout 5000 ms
  (`NewHealthCheck`, `healthcheck.go:200-201`).
- `Proxy.URLTest` (`adapter/adapter.go:166`) **opens a full tunnel through the
  proxy to the test URL** (`adapter.go:210`), does an HTTP HEAD over it via a
  throwaway `http.Transport` whose dialer returns the single instance
  (`adapter.go:229-239`), then closes it (`adapter.go:214-216`) and closes idle
  connections (`adapter.go:249`). With `unified-delay` it issues a second HEAD
  on the same connection (`adapter.go:259-267`). Alive/delay history: 10
  entries (`defaultHistoriesNum`, `adapter.go:26`).
- Consequence: a probe is a *cold* connection for classic transports (it pays
  the full handshake and warms nothing reusable next time, beyond OS/TLS
  session caches — the latter **unverified**). For pooled transports (gun,
  sing-mux, hysteria2, tuic, anytls) the probe's stream rides the existing
  persistent session, exercising the path; for anytls this also matters for
  its idle-session pool by design (`anytls.go:114-123`). Mihomo does not
  proactively ping sing-mux sessions (no knob found, §2.3).
- Group behavior:
  - `url-test` picks the fastest (`URLTest.fast`, `urltest.go:103`);
  - `fallback` walks the list for an alive proxy (`findAliveProxy`,
    `fallback.go:105`);
  - a plain `select` group does **not** move away from a dead selected node:
    `Selector.DialContext` dials whatever is selected, failure or not
    (`selector.go:24-29`, `selectedProxy` `selector.go:103-111`).
  - "Instant failover" nuance: a failed dial triggers a health check
    **immediately only for "connection refused"**; otherwise failures are
    counted and the check fires after `maxFailedTimes` within the test-timeout
    window (`GroupBase.onDialFailed`, `groupbase.go:265-297`).
  - Per-attempt selection: within one dial attempt a group selects and dials
    **once** — `Fallback.DialContext` picks via `findAliveProxy`, dials,
    schedules failure handling and returns the result (`fallback.go:32-51`);
    `URLTest.DialContext` does the same via `fast` (`urltest.go:57-76`).
    Neither retries the next proxy inside the call. Re-selection on the same
    client connection happens only across the tunnel's own retry attempts
    (`tunnel.handleTCPConn` wraps the dial in `retry`,
    `tunnel/tunnel.go:574-577`) — each retry re-enters the group's
    `DialContext` and re-runs selection. How many retries the tunnel makes
    was **not traced**; treat current-request failover as best-effort.
- Configurable: provider `health-check: {enable, url, interval, lazy, timeout,
  expected-status}`; group `{url, interval (default 300 s, parser.go:172),
  timeout, lazy (default true, parser.go:52), expected-status, tolerance,
  filter}` (`adapter/outboundgroup/parser.go:31-40`).

## 4. DNS and node-server resolution

- Mihomo's DNS has a TTL cache (`dns.Resolver.cache`, `dns/resolver.go:48`);
  `ExchangeContext` serves hits from cache (`dns/resolver.go:151-171`).
- Outbound dials resolve the node's server hostname through
  `component/dialer.parseAddr` (`component/dialer/dialer.go:380`), whose
  default resolver for proxy-server hosts is
  **`resolver.ProxyServerHostResolver`** (`dialer.go:386-397`; declared at
  `component/resolver/resolver.go:21-22`, "only nil when DefaultResolver is
  nil"). `tcp-concurrent` enables happy-eyeballs
  (`parallelDialContext`, `dialer.go:316`; dispatch at `dialer.go:58-73`).
- Consequence for warming: a pre-resolving query only helps if it goes through
  the **same** `ProxyServerHostResolver` cache that `parseAddr` reads. Whether
  netivri can drive that resolver instance directly from outside the mihomo
  `dns` package (it is an exported variable, but who instantiates/populates it
  under our config) is **unverified**. Fallback option: let the first real
  dial (or a probe) pay the lookup, and rely on the TTL cache afterwards.
  Cache clearing exists via `resolver.ClearCache`
  (`component/resolver/resolver.go:233`). Whether the cache serves
  stale-but-usable entries on lookup failure is **unknown**.

## 5. Recommendation

### #15 — background health checks + keep-warm (feasible, no fork)

1. **Prefer node configs with persistent warm state**: `network: grpc` (gun)
   with `ping-interval`, `hysteria2`, `tuic` (`heartbeat-interval`,
   optionally `reduce-rtt`), `anytls` (tuned `min-idle-session`), snell v2
   (or v4 with `reuse: true` — built-in pool, `snell.go:252,287-300`),
   ssh/hysteria v1 where present; add `smux` where the server supports it. For these, every
   client connection after the first is already ~warm.
2. **Run a netivri-owned probe loop** (not mihomo's lazy provider checks,
   which skip when idle — `healthcheck.go:50-56`) for the selected node +
   top-K alternates: call `C.Proxy.URLTest` every 30-60 s, timeout 5 s
   (mihomo default, `healthcheck.go:200-201`). This exercises persistent
   sessions and keeps alive/delay state fresh. A plain `DialContext`+close
   is **not** a cheaper equivalent: protocol-handshake completion on the
   returned connection cannot be assumed. Adapters differ — vless defers its
   request to the first `Write` (`vless.Conn.Write`,
   `transport/vless/conn.go:46-51`; `newConn` just wraps,
   `conn.go:159-178`), while trojan writes its header inside
   `DialContext` (`Trojan.DialContext` → `StreamConnContext` →
   `streamConnContext`, `trojan.go:180-194,220-234`). Either way,
   dial-and-close validates nothing (an unexercised or half-set-up tunnel
   can test "alive") and it updates no URL-test alive/delay state
   (`Proxy.URLTest` is the only writer, `adapter/adapter.go:166-198`).
3. **Group topology for selection + failover**: do not point the selector
   directly at a single node (a selector stays on a dead node,
   `selector.go:24-29`). Put the selected node + top-K alternates in a
   `fallback` (deterministic order) or `url-test` group and select the
   *group*. Expected behavior: fallback re-picks per request from alive
   proxies (`fallback.go:105`); failed dials feed `onDialFailed`, which
   triggers a health check immediately on "connection refused" and after
   `maxFailedTimes` otherwise (`groupbase.go:265-297`) — so failover is fast
   but **not guaranteed instant** for non-refusal errors; probes keep the
   alive-flags accurate in between.
4. Keep-alive tuning: global `keep-alive-idle/interval`; per-node
   `ping-interval` (gun), `heartbeat-interval` (tuic), anytls
   `idle-session-*`, so parked sessions survive NAT idle timeouts.

### #16 — pre-dial pool: verdict per class

- **(a) Raw TCP pre-dial pool — feasible WITHOUT a fork.** Supply a pooling
  `C.Dialer` via `adapter.WithDialerForAPI` at proxy construction
  (`adapter/parser.go:268-272`; consumed by `BasicOption.NewDialer`,
  `base.go:209-219`). The adapter's raw dial then pops a warm TCP conn
  (e.g. `Vless.dialContext` → `v.dialer.DialContext`, `vless.go:348`).
  Scope: saves ~1 RTT (TCP) per client connection; TLS/REALITY and the
  protocol header are still per-request. Must replicate `DialOptions()`
  (`base.go:142-170`) since `DialerForAPI` overrides them, and must handle
  dead pooled conns itself. Applies to every adapter that dials raw TCP
  through `b.dialer` (vless/vmess/trojan tcp, ss non-kcptun, ssr, http,
  socks5, snell with reuse off); useless for already-pooled transports
  (gun/xhttp/QUIC/sing-mux/snell-with-reuse) and for L3 overlays.
- **(b) TCP+TLS/REALITY pre-establishment — needs a fork.** The TLS wrap is
  unconditional inside `StreamConnContext` (`vless.go:258-262`); no exported
  skip/wrap-only entry point exists. Fork shape: split `StreamConnContext`
  into transport-wrap and header-write steps. Would save the additional
  1-2 RTT of TLS 1.3 / REALITY per client connection for tcp-tls nodes.
- **(1) Fixed-destination parking of a completed tunnel — feasible WITHOUT a
  fork**, as a destination-keyed pool behind a wrapper adapter's
  `DialContext` (`constant/adapters.go:126-151` imposes no restriction on
  returning an existing `C.Conn`; the wrapper-adapter pattern itself is
  established by `SingMux`, `singmux.go:50/94`). Requirements, all netivri-side:
  - the parked tunnel is bound to the destination committed at its original
    dial (`parseVlessAddr` `vless.go:434-459`, `trojan.writeHeaderContext`
    `trojan.go:197`) — serve only dials to that exact destination;
  - a returned `C.Conn` handed to the tunnel layer is consumed by that
    request; the pool must not hand the same conn to a second concurrent
    dial, and sequential reuse of a consumed stream is protocol- and
    server-dependent (**unknown** — no source evidence either way; treat as
    unsafe until measured);
  - lifecycle: idle timeout, dead-peer detection, close-on-node-switch —
    mihomo offers no help here, and the pool must own all of it. Note the
    existing cleanup mechanism: `adapter/parser.go:248` only *constructs*
    `AutoCloseProxyAdapter`; closure actually happens via a GC finalizer
    (`NewAutoCloseProxyAdapter`, `adapter/outbound/base.go:410-416`,
    `runtime.SetFinalizer`), and dials can pin the adapter to returned
    connections via `AddRef` (`autoCloseProxyAdapter.DialContext`,
    `base.go:379-387`). A parked `C.Conn` holding such a reference delays
    finalization — so netivri's pool must invalidate and close parked
    tunnels itself on node switch or config reload, not rely on removal
    from configuration.
  Good for: repeated dials to few fixed destinations (e.g. the probe URL, a
  hot API host). Cannot: serve a different destination, be shared across
  nodes, or survive the underlying transport dying.
- **(2) Retargeting a committed tunnel to a different destination —
  impossible, with or without a fork.** The destination bytes are already
  sent to the server; no local code change redirects an established tunnel.
  Only a fresh dial produces a new destination.
- **Overall recommendation for v1: skip the pool; implement (a) only if
  measurements show the remaining TCP RTT matters for tcp-only nodes
  (e.g. plain shadowsocks).** The dominant win is choosing pooled transports
  (#15.1), which achieves warm TTFB with zero fork.

## 6. Measurement plan

Three distinct quantities — do not conflate them:

1. **Logical dials**: client-connection count reaching
   `ProxyAdapter.DialContext`. Observable with a thin counting wrapper around
   the adapter (same pattern as `SingMux`, `singmux.go:94`). This is *not* a
   handshake count: gun/sing-mux/QUIC pool many logical dials onto one
   physical connection.
2. **Physical upstream connections / handshakes**: TCP SYNs / QUIC handshakes
   to the node. Not exposed by mihomo (gun transport count and sing-mux
   session internals are unexported); measure via OS tooling (packet capture
   or socket tables) during the benchmark, or add netivri-side counting
   inside its own pooling dialer for the (a)-class paths it controls.
3. **TTFB**: cold vs warm. Cold = first request after `netivri up`; warm =
   steady state with the #15 probe loop on. Per protocol family
   (tcp-tls, grpc, hysteria2, tuic, anytls, smux, ss).

`netivri status` additions (schema bump required), named to reflect what they
actually are:

- `logical_dials` — wrapper counter (quantity 1).
- `last_probe_ms`, `probe_interval`, `alive`, `delay_history` — all
  readable through the `C.Proxy` interface: `AliveForTestUrl`
  (`constant/adapters.go:169`), `ExtraDelayHistories`
  (`constant/adapters.go:171`), `LastDelayForTestUrl`
  (`constant/adapters.go:172`). The only exception is
  `DelayHistoryForTestUrl` for a specific URL
  (`adapter/adapter.go:78`) — that method lives on the concrete
  `adapter.Proxy` (it reads `p.extra` directly, `adapter.go:81`) and is
  absent from the interface; through `Proxy.Adapter()` it is unreachable
  (`adapter.Proxy.Adapter` returns the inner `ProxyAdapter`,
  `adapter.go:41-43`). Use `ExtraDelayHistories()` keyed by test URL, or
  keep a reference to the concrete `*adapter.Proxy` from construction.
- `inferred_warm` (bool) — **inference, not transport state**: last probe
  succeeded within the node's idle-keepalive window. A transport-level
  "session exists" signal would require mihomo exposure that does not exist
  today (**unknown** how to obtain without a fork; do not label it `warm`).

## 7. Explicit unknowns / not verified

- hysteria2 QUIC keep-alive: not found in adapter options; whether sing-quic
  sets `KeepAlivePeriod` internally was not traced (`hysteria2.go:201-206`
  sets only windows).
- Whether TLS session resumption shortens repeat handshakes for tcp-tls
  probes (plausible, not measured).
- xhttp/XMUX runtime idle behavior under `h-keep-alive-period`
  (`vless.go:131-139`); `transport/xhttp` not read line-by-line.
- Whether mihomo's DNS cache serves stale entries on lookup failure.
- Whether netivri can practically drive `resolver.ProxyServerHostResolver`
  from outside mihomo's dns setup (§4).
- wireguard/openvpn/easytier/tailscale/zerotier internals (L3 overlays):
  only construction-site evidence, not read line-by-line (§2.2).
- Whether a consumed fixed-destination tunnel can be safely reused
  sequentially (protocol/server dependent) — see §5/#16(1).
- How many retry attempts `tunnel.handleTCPConn`'s `retry` wrapper makes
  (§3).

## Implementation (tasks #15–#17)

The standalone `startWarm` helper probes Mihomo leaf proxies through
`C.Proxy.URLTest`, records the latest health state and counters, and returns a
copied, sorted node snapshot from `Stats`. Defaults are a 45-second interval,
five-second probe timeout, the gstatic URL, and a 30-second pool TTL. Call it
after `executor.ApplyConfig`, retain the warmer, and call `Close` before
`executor.Shutdown`; it is idempotent and bounds its waits. `core.Start`
starts it when `Options.Warm` is set (after `ApplyConfig` and the listener, so
a failed listen leaves nothing behind) and `Instance.Close` stops it before
`executor.Shutdown`; `Instance.WarmStats` exposes the counters. The daemon sets
`Options.Warm` (probes only, no pool) whenever accounts route through country
nodes, and `status` prints the counters as `warm`.

A round probes its nodes concurrently, at most `ProbeConcurrency` at a time
(default 8). A node that failed twice in a row is skipped in the following
rounds, one more round after each further failure up to three, and is probed
every round again after one success. `WarmOptions.OnProbe` receives every
result (internal proxy name, success, delay); a probe cut short by shutdown is
neither counted nor reported. The results feed the sticky route groups and the
statistics store (docs/design/node-stats.md).

With `PoolSize > 0` and a node, the optional pool installs a proxy adapter for
`netivri-node`, preserving other proxies and providers. It parks raw TCP
sockets to the configured upstream address only; supported dial options are
applied and unsupported transports/options are rejected. Reuse saves only TCP
setup: proxy protocol, authentication, TLS and destination negotiation still
occur for each dial. Idle sockets are checked for closure, expired, and closed
on shutdown. This is not a pool of completed tunnels and does not guarantee a
peer has not closed a socket between checking and use. The liveness read of an
idle socket (up to 1 ms) runs outside the pool mutex, so one slow check does
not hold up other dialers.

Unit tests cover option normalization, leaf filtering, health snapshots and
run-loop shutdown. Integration tests exercise actual `Start`/Mihomo probes
through a loopback fake SOCKS5 upstream, including alive/dead results; pooled
node round trips, hit/discard accounting and shutdown; and a bench-tagged
through-node comparison against a delayed loopback fake upstream. Local
builds, tests, vet and benchmarks were not run, per repository policy.
