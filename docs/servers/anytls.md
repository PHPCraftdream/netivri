# AnyTLS server

AnyTLS is a multi-user, TLS-protected TCP server. Each account has its own generated password; one listener serves all configured accounts. The listener's authentication context maps the authenticated username to Mihomo's `metadata.InUser`, so traffic enters that account's normal route. AnyTLS UDP support in the upstream handler does not enable server UDP here: the core wrapper drops and counts UDP from every server tunnel to prevent account-route bypass.

## Configure and run

The command lines below describe the planned product interface, not an implemented CLI:

```text
netivri server add --protocol anytls --name edge --account alice --port {PORT} --bind 127.0.0.1 --public-host {HOST}
netivri server link edge --account alice --host {HOST}
netivri server rotate edge --account alice
```

`{PORT}` is a TCP port and `{HOST}` is the client-reachable DNS name or address. Loopback binding keeps this example private; expose a reachable bind only intentionally. A link grants its recipient use of that account's route, so handle it as a credential.

Optional parameters:

| Parameter | Meaning |
| --- | --- |
| `sni` | TLS server name advertised to clients. Optional. |
| `padding_scheme` | Omit or set empty to use the vendor default (`stop=8`, etc.). A custom value is at most 4096 bytes, with newline-separated directives and no trailing blank line. It requires exactly one `stop={N}` (`N` is decimal `0..1000`) and packet directives `{K}={RANGE[,RANGE...][,c]}` where `{K}` is canonical decimal `0..999`, unique, and each range has decimal endpoints `1..65535` separated by `-`; optional `c` is a literal item. Duplicate keys are rejected. |
| `cert_file`, `key_file` | Existing TLS certificate and key paths; must be supplied together. |
| `public_host` | Optional host metadata for generated certificate SAN. If absent or empty, `sni` is used when nonempty; otherwise `localhost`. |

`alpn` is not accepted. TLS validation is shared with other TLS server protocols; see [TLS material](tls.md).

The generated password uses 24 random bytes encoded as unpadded base64url (32 characters). The shared TLS helper generates a self-signed ECDSA P-256 certificate with ten-year lifetime unless file material is supplied.

## Links and Mihomo

Generated links have this shape (values shown are placeholders):

```text
anytls://{PASSWORD}@{HOST}:{PORT}?sni={SNI}&insecure=0&hpkp={PIN}#{NAME}
```

The `sni` query parameter is omitted when unset. `hpkp` is the actual leaf certificate SHA-256 fingerprint. The vendor URI converter maps `hpkp` to Mihomo's `fingerprint`; that is distinct from `client-fingerprint` (the latter selects a client TLS fingerprint, here `chrome`).

A Mihomo proxy snippet is emitted in this form:

```yaml
proxies:
  - name: "{NAME}"
    type: anytls
    server: "{HOST}"
    port: {PORT}
    password: "{PASSWORD}"
    sni: "{SNI}"
    fingerprint: "{PIN}"
    client-fingerprint: chrome
    idle-session-check-interval: 30
    skip-cert-verify: false
    udp: false
```

The `sni` line is emitted even when empty in the snippet. The snippet's explicit `udp: false` is important: importing the generated URI through a vendor converter may set UDP true. Disable UDP in imported client configurations because netivri server UDP is dropped.

Mihomo's AnyTLS client adapter and `hpkp` conversion are supported by inspected vendored source. This establishes bundled Mihomo compatibility, not runtime verification; the tests are CI-only. Mihomo-based clients such as Mihomo Party or Clash Verge Rev may work when their bundled Mihomo supports AnyTLS and fingerprint pinning; no runtime claim is made for those applications. Other clients need AnyTLS plus `hpkp` pin support, or a certificate trusted through the normal CA system.

## TLS security

A leaf pin bypasses normal certificate-chain, hostname, and expiry validation in Mihomo. Distribute the pin safely and independently, monitor certificate expiry, and distribute a new link/pin on certificate rotation. Do not substitute `skip-cert-verify`; it disables verification rather than pinning. A trusted CA certificate can instead use ordinary CA validation. Protect the private key and treat links as credentials.

## Runtime and tests

The server passes each username through the AnyTLS listener's auth context as `InUser`; the core then applies its standard account routing. The core tunnel wrapper drops and counts UDP, including packets that upstream's handler might otherwise support.

Unit and integration tests are authored for server behavior. Integration coverage uses the Mihomo client adapter, loopback node recorders and a local echo endpoint to check Alice/Bob routing, wrong-password rejection, password rotation, listener close/port release, and global/account client-policy denial and drop counters. Certificate trust coverage exercises both leaf pinning and CA trust via `TrustForTest`. Per [testing policy](../testing.md), these tests run only in GitHub CI; they were **not run** for this documentation change.

## Known limitations

Source-based inference, not a runtime observation: vendored `listener/anytls/server.go` reads initial authentication bytes with a single `ReadOnceFrom` call, so fragmented TCP delivery might cause valid authentication to be rejected. The vendor listener also has a `closed` boolean; core lease close ordering may provide a happens-before edge when close is called first, but transient `Accept` errors and concurrent `Close` are not established as synchronized. These are not confirmed runtime races or failures; CI race testing is needed. Vendored source is outside this change's scope; see [third-party source rules](../../third_party/README.md).
