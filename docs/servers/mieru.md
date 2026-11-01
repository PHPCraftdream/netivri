# Mieru server

Mieru serves multiple accounts on one TCP port over one TCP listener; port ranges and multi-port bindings are not configured by this implementation. Authenticated users carry their login into the core and use their own account route. Mieru provides password-authenticated protocol encryption and multiplexing without requiring a certificate, making it a certificate-free encrypted TCP option. This implementation intentionally supports TCP only even though upstream Mieru supports UDP: UDP transport is rejected, and UDP payloads through the core wrapper are dropped rather than routed around the account route.

## Configure

```text
netivri server add --protocol mieru --name edge --account alice,bob --port {PORT} --bind 127.0.0.1 --param transport=TCP --param multiplexing=MULTIPLEXING_OFF
netivri server link edge --account alice --host {HOST} --format mihomo
netivri server rotate edge --account alice
```

These commands describe the server interface. A loopback bind is private; a reachable bind exposes the endpoint. Mieru passwords are bearer credentials: protect the secrets store and exported client configuration, and rotate credentials if exposed. `server link --format uri` emits a standard Mieru `mierus://` link; `--format mihomo` emits the authoritative YAML form. Never put passwords in logs or unrelated output.

## Parameters and secrets

`transport` defaults to `TCP`; values are normalized case-insensitively and only TCP is supported. `multiplexing` is an optional Mihomo client-side setting; omitted means the dependency's default behavior. Accepted explicit values are exactly, case-sensitively, `MULTIPLEXING_DEFAULT`, `MULTIPLEXING_OFF`, `MULTIPLEXING_LOW`, `MULTIPLEXING_MIDDLE` and `MULTIPLEXING_HIGH`; lowercase or mixed-case spellings are rejected. It configures client multiplexing; it is not a server-side listener setting. Unknown parameters are rejected. This implementation creates no TLS material and has no X.509/TLS configuration.

Each account receives an independently generated random password from 32 random bytes, encoded as unpadded base64url (at least 24 bytes of credential entropy). The password is stored only in the server secrets file; it must be supplied to the authenticated Mieru client.

## Client configuration

`server link --format uri` emits `mierus://{USERNAME}:{PASSWORD}@{HOST}?port={PORT}&protocol=TCP[&multiplexing={LEVEL}]#{NAME}`. Credentials are percent-escaped. Exactly one port/protocol pair is emitted; Mieru links can represent repeated pairs for port ranges, but this server implementation exposes one TCP port only. Importers such as Mihomo set `udp: true` for this URI format, whereas the authoritative Mihomo YAML emitted by `--format mihomo` explicitly sets `udp: false`. Core drops UDP payloads, so do not enable UDP on the URI-imported proxy. The importer may read handshake-mode or traffic-pattern fields from other Mieru links; netivri does not add these unsupported server parameters. `--format mihomo` emits a YAML `proxies` wrapper with one Mieru proxy named exactly for the server (`spec.Name`) and fields for `server`, `port`, `transport`, `username`, and `password`, plus explicitly configured client multiplexing. The listener factory uses the supplied `ListenConfigForAPI` to receive the core's staged TCP listener, so it participates in the core's socket-broker staging model rather than opening an unrelated listener. The Mieru module's mux close path closes underlays; therefore, while the broker can stage and roll back listeners, Mieru sessions on an old listener may end when it is stopped during a committed reconfiguration. Do not assume generic TCP connection-drain behavior guarantees Mieru sessions survive rotation/reconfigure. That Mihomo YAML snippet is authoritative for Mihomo-compatible clients, including Clash Verge Rev. Native Mieru clients need a manual client configuration using the same server address, TCP port, username, password and TCP transport; multiplexing is a client-side option and needs no matching listener setting.

This endpoint uses Mieru's own password-authenticated protocol encryption over TCP; it does not use X.509 certificates or TLS. Choose a TLS-capable protocol when TLS camouflage is required.

## Testing

The tagged loopback integration test is designed to run in CI. It uses Mihomo's Mieru outbound against one server started through `core.Start`, and checks Alice/Bob routing through separate loopback SOCKS recorders, wrong-password rejection, Alice password rotation with fresh clients, global and account allowlist drops, Bob remaining usable after Alice filtering, connection drain, failed-stage rollback while preserving the old listener, and TCP port release. Unit coverage checks parameter validation and exact multiplexing enum spelling, generated secrets, URI round-tripping through Mihomo's converter and subscription parser, and Mihomo YAML output. The tests have not been run locally; all tests run in CI.

A real-angel end-to-end check is deferred to a separate task. It should exercise the generated `--format mihomo` YAML as a client subscription against a loopback server, with exact TCP transport and multiplexing parameters; this loopback core integration coverage does not claim to replace that check.
