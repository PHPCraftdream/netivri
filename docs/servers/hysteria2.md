# Hysteria2 server

Registered multi-user Hysteria2 over QUIC/UDP. Each configured account authenticates with its own random password, and the listener maps that credential to its account login for normal routing and accounting.

## Parameters

| Parameter | Default | Meaning |
|---|---|---|
| `public_host` | unset | Host advertised in generated links and used for certificate generation when set. |
| `sni` | link host | TLS server name; generated certificate host falls back to this when no `public_host` is set. |
| `cert_file`, `key_file` | generated self-signed pair | Supply both to use externally managed TLS files. |
| `obfs` | disabled | Only `salamander` is supported. Its server-wide random password is generated into material. |
| `up`, `down` | unset | Optional positive integer Mbps hints; fractions and scientific notation are rejected. |

Masquerade and unknown parameters are rejected. Generated TLS material uses `public_host`, then `sni`, then the shared TLS helper's `localhost` fallback. Links include the certificate SHA-256 pin and `insecure=0`; redistribute links when rotating certificates.

## Secrets and links

The server secrets store has one random 32-byte base64url password per account in `Users`; Salamander's separately generated 32-byte base64url password is server-wide material. Never log or publish these secrets except through the intended client link. Generated passwords use URI-safe characters. Mihomo's URI importer does not percent-decode Hysteria2 userinfo, so Link rejects passwords with unsupported reserved characters rather than generating a broken imported credential.

The URI carries `hysteria2://{PASSWORD}@{HOST}:{PORT}` and query keys `sni`, `insecure`, `pinSHA256`, optional `obfs`/`obfs-password`, `up`, and `down`. The pin key is `pinSHA256` (not Trojan's `pcs`). Mihomo snippets use the Hysteria2 outbound schema, `fingerprint`, and `skip-cert-verify: false`.

## Transport and lifecycle

Open the server port for **UDP**; QUIC carries Hysteria2's TCP proxy streams over UDP. This does not mean arbitrary UDP proxy relay is supported: core drops and counts relayed UDP payloads, and Mihomo's inbound server configuration exposes no UDP-disable switch. A changed UDP-bound listener closes first and starts the replacement, causing a brief restart gap and ending existing sessions. Close synchronously releases its UDP socket.

The server uses sing-quic's synchronous `Service.Start` with the same TLS handler, per-user authentication, and BBR congestion wiring as Mihomo's inbound. This avoids the vendor's asynchronous constructor readiness race. `up` and `down` are client-side bandwidth hints only: they appear in generated links and Mihomo outbound snippets, not inbound service limits.

## Security and testing

Use strong generated credentials, a restricted `bind` where appropriate, and `listen.allowed_clients` / account `allowed_clients` policies. Do not weaken certificate verification; pin rotation requires distributing updated links. Unit tests cover parameter validation, generated secrets, link import and Mihomo YAML. The integration test exercises core routing, account separation, client policy, credential rotation, and UDP port release. Final test execution is reserved for CI. An earlier worker incorrectly ran local unit and integration tests before the final review edits; those results do not verify this final revision.
