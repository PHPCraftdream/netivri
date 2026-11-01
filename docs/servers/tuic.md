# TUIC server

TUIC is registered as a single-user, UDP-bound server. It serves exactly one resolved account per listener; the TUIC UUID is authenticated by the protocol and mapped by a wrapper to that configured netivri login. To serve two accounts, configure two separate endpoints.

## Parameters and credentials

Supported parameters:

- `sni`: TLS server name; defaults to the public connection host in links.
- `alpn`: comma-separated ALPN values; defaults to `h3`.
- `congestion`: `bbr` (default), `cubic`, or `new_reno`.
- `udp_relay_mode`: `native` (default) or `quic` (TUIC client's UDP relay mode).
- `public_host`: certificate SAN and public host hint; certificate generation prioritizes this over `sni`.
- `cert_file` and `key_file`: an existing matching TLS certificate/key pair, supplied together.
- `udp`: generated Mihomo snippets set this to `false`; the listener itself remains UDP-bound.

Generated TLS material is a self-signed P-256 certificate with a SHA-256 fingerprint. Generated user credentials are a UUIDv4 and 32 random password bytes. Keep the fingerprint available to clients when using generated self-signed material.

Example configuration:

```text
servers: [
    {
        name: edge-tuic
        protocol: tuic
        bind: 0.0.0.0
        port: 443
        accounts: alice
        public_host: "{PUBLIC_HOST}"
        params: {
            sni: "{TLS_NAME}"
            alpn: h3
            congestion: bbr
            udp_relay_mode: native
        }
    }
]
```

`netivri server add --protocol tuic --name edge-tuic --account alice --port 443 --public-host '{PUBLIC_HOST}'` creates the endpoint; use `netivri server link edge-tuic --host '{PUBLIC_HOST}'` for its URI.

## Client configuration and pin limitation

The TUIC URI has no supported certificate-pin query in the vendored importer. The converter imports UUID/password, endpoint, SNI, ALPN, congestion, UDP relay mode and sets the outbound `udp: true`; it does not import a certificate fingerprint, `skip-cert-verify`, `certificate`, or `private-key`. Do not rely on URI import for a self-signed certificate: add the fingerprint to the resulting client configuration and keep certificate verification enabled.

Mihomo YAML (for example, in Mihomo or a configurable client such as Clash Verge Rev; compatibility is not asserted without a client test). Replace placeholders with actual link/account values. Obtain the fingerprint from the server's generated material or calculate it from the certificate; don't use a URI lookup as pin source.

```yaml
proxies:
  - name: edge-tuic
    type: tuic
    server: "{PUBLIC_HOST}"
    port: 443
    uuid: "{TUIC_UUID}"
    password: "{TUIC_PASSWORD}"
    sni: "{TLS_NAME}"
    alpn: [h3]
    congestion-controller: bbr
    udp-relay-mode: native
    fingerprint: "{CERTIFICATE_SHA256_FINGERPRINT}"
    skip-cert-verify: false
    udp: false
```

The URI converter does not import the fingerprint, `skip-cert-verify`, `certificate`, or `private-key`; see its source at `third_party/mihomo/common/convert/converter.go` lines 118–159 (CLI: `--format mihomo`). Keep certificate verification enabled; manually provide the actual certificate fingerprint in the client config if its policy supports pinning.

## UDP behavior and lifecycle

TUIC carries its QUIC transport over UDP. Open the configured port in the host/network firewall for **UDP**, not merely TCP. TUIC's UDP transport does not enable UDP relay payload forwarding through netivri: the shared core server wrapper refuses and counts inbound UDP payloads so they cannot bypass account routing.

UDP-bound listeners use a real packet socket. Reconfiguration closes the old listener before binding its replacement; the socket is synchronously freed, but there is a brief restart gap and existing QUIC sessions end. If replacement fails, restoration of the previous UDP listener is best-effort and may be reported incomplete.

Authored integration coverage exercises two routed accounts on separate ports, wrong-credential rejection, code paths, reconfiguration/rotation and port reuse. It has not been run here.
