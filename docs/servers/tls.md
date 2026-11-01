# TLS material

TLS server protocols share `internal/servers` certificate material. Generated
material uses a self-signed ECDSA P-256 certificate, random positive serial,
server-auth usage, and a ten-year lifetime. It includes a SAN for the requested
DNS name or IP. An empty host defaults to `localhost` and `127.0.0.1`; a
non-empty invalid host is rejected. Generated PEM is stored under `cert_pem`
and `key_pem`; `fingerprint` is the lowercase SHA-256 hash of the leaf DER.

Supply `cert_file` and `key_file` together for an existing pair. Paths are
resolved to absolute paths against the process working directory at creation;
files must be readable and contain a matching certificate/key pair. Material
retains paths and fingerprint, not file contents. Mihomo resolves listener
paths against its home and enforces `SAFE_PATHS`; ensure the files are under the
configured Mihomo home or allowed paths. This helper does not mutate Mihomo's
global path state or enforce that policy, since the home may be set later.

Use `TLSFiles` for the PEM-or-path arguments consumed by listeners. CA-issued
certificates work with ordinary CA validation. Self-signed certificates need
client support for certificate pinning; do not compensate with
`skip-cert-verify`. Mihomo's leaf fingerprint verifier accepts a matching leaf
pin directly, bypassing its normal chain, hostname, and expiry validation.
Monitor expiry independently. `TLSExpiry` parses the current leaf and
`RegenerateIfExpiring` renews generated PEM when remaining lifetime is at most
the requested window, preserving unrelated fields. Rotation changes the pin:
regenerate client links and redistribute updated pins. External file renewal is
manual and likewise changes the leaf pin. The private PEM exists in returned
server material; store it only in the server secrets store, protected as a
0600 file, and treat generated links and secret outputs as sensitive. A
fingerprint identifies a certificate but is not secret.

Tests importing `internal/servers/servertest` can call `TrustForTest(t, material)`
to add PEM- or file-backed certificates to Mihomo's global CA pool; cleanup
restores the pool. Such tests must not run in parallel with other tests that
mutate that global pool. `FreePort` binds loopback TCP port 0 briefly and returns
the selected port; `StartOnFreePort` retries address-in-use failures up to
three times. The close-before-use interval means a port race remains possible,
so the retry helper reruns only when listener startup reports the port in use.

There is no ACME integration: these server protocols need certificate material
without adding domain validation, background network access, account state, or
renewal lifecycle management. Mihomo's generic random TLS keypair helper is not
used because it does not provide the required SAN, serial, or ten-year
validity. Fingerprints use Mihomo's `ca.CalculateFingerprint`.

`ValidateTLSParams` checks that file paths are paired and non-empty, validates
optional `sni` as a DNS name or IP, and accepts optional comma-separated `alpn`
tokens containing 1–255 printable non-space ASCII bytes. Other parameters
remain the protocol implementation's responsibility. Errors name parameter
keys as appropriate and never include path, secret, or value contents.

API:

```go
func NewTLSMaterial(params map[string]string, host string) (map[string]string, error)
func TLSFiles(material map[string]string) (certificate, privateKey string)
func TLSExpiry(material map[string]string) (time.Time, error)
func TLSFingerprint(material map[string]string) string
func RegenerateIfExpiring(material map[string]string, host string, within time.Duration) (map[string]string, error)
func ValidateTLSParams(params map[string]string) error
```
