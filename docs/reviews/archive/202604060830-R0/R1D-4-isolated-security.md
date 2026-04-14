# OIDC Adapter Independent Security Review

Scope: `adapters/oidc`

Method: static review only. I inspected the adapter source, its tests, and the repo's current OIDC-related guidance. I did not read any existing files under `docs/reviews/`, and I did not modify business code.

## Findings

### 1) Discovery metadata is not bound to the configured issuer
Severity: High

The adapter fetches `/.well-known/openid-configuration` from `Config.IssuerURL` and then trusts the returned `issuer`, `jwks_uri`, `token_endpoint`, and `userinfo_endpoint` without checking that the discovery document's `issuer` exactly matches the configured issuer. The same untrusted metadata is then used to drive token exchange, JWKS retrieval, and UserInfo requests.

Evidence:
- `adapters/oidc/provider.go:71-123`
- `adapters/oidc/token.go:28-47`
- `adapters/oidc/userinfo.go:25-43`
- `adapters/oidc/verifier.go:179-197`

Why this matters:
- OIDC Discovery requires the returned `issuer` to exactly match the issuer used for discovery, and OIDC Core requires the ID token `iss` to match that issuer.
- Without that binding, a malicious or poisoned discovery response can redirect the adapter to attacker-chosen endpoints and attacker-chosen keys.
- In practice, that means forged ID tokens may be accepted if the attacker can influence discovery, and the token exchange can be steered to an endpoint that receives `client_secret`.

Relevant specs:
- OpenID Connect Discovery 1.0, section 4.3 and 7.2: exact issuer match and impersonation protections.
- OpenID Connect Core 1.0, section 3.1.3.7: `iss` must match the discovered issuer.

Recommendation:
- Reject discovery documents whose `issuer` does not exactly equal `Config.IssuerURL`.
- Treat discovered endpoints as untrusted until they pass issuer/origin checks.
- Prefer a strict allowlist for endpoint origins instead of following arbitrary metadata URLs.

### 2) Outbound transport accepts cleartext and arbitrary discovered URLs
Severity: High

`NewProvider` builds a plain `http.Client` with only a timeout. There is no scheme validation for `IssuerURL`, and the adapter happily follows whatever scheme and host are present in the discovery document. That includes `http://` endpoints and cross-host URLs.

Evidence:
- `adapters/oidc/provider.go:44-52`
- `adapters/oidc/provider.go:71-79`
- `adapters/oidc/token.go:46-54`
- `adapters/oidc/userinfo.go:36-43`
- `adapters/oidc/config.go:43-51`

Why this matters:
- OIDC Discovery requires TLS for metadata retrieval and certificate validation for the issuer URL.
- Allowing non-HTTPS issuers or non-HTTPS discovered endpoints exposes authorization codes, bearer tokens, and `client_secret` to passive interception or active MITM.
- Because the token request posts `client_secret` in the form body, a malicious discovery response or misconfigured issuer can exfiltrate the secret to an attacker-controlled endpoint.

Relevant specs:
- OpenID Connect Discovery 1.0, section 7.1: TLS is required.
- OpenID Connect Discovery 1.0, section 7.2: certificate checks prevent impersonation and DNS-based attacks.

Recommendation:
- Require `https` for issuer and discovered endpoints.
- Reject any discovery document whose endpoints are not HTTPS, or whose host/origin is outside the expected issuer trust boundary.
- Consider a hardened transport policy that disables redirects across origins and, if possible, pins the expected host.

### 3) JWKS rotation is not actually enforced
Severity: High

The verifier has `JWKSCacheTTL` and `fetchAt` fields, but neither is used in the key lookup path. `getKey` only refreshes JWKS when the requested `kid` is missing. Once a key is cached, it remains trusted indefinitely, even after the provider rotates or revokes it.

Evidence:
- `adapters/oidc/config.go:21-24,37-39`
- `adapters/oidc/verifier.go:48-55`
- `adapters/oidc/verifier.go:155-177`
- `adapters/oidc/verifier.go:226-251`
- `adapters/oidc/verifier.go:230`

Why this matters:
- A stale key cache defeats rotation and revocation. If an old private key is compromised, tokens signed with that key can continue to verify until the process restarts.
- This also means the adapter does not meet its own documented "JWKS key rotation" claim.

Recommendation:
- Enforce TTL-based refresh using `JWKSCacheTTL`.
- Refresh JWKS proactively on age, not only on cache miss.
- Replace the key set atomically and discard stale keys when a refreshed JWKS no longer advertises them.

### 4) ID token claim validation is incomplete for OIDC flows
Severity: Medium

The verifier checks signature, `iss`, and `aud`, but it does not validate OIDC-specific claims such as `nonce` or `azp`. The adapter also does not accept an expected nonce from the caller, so it cannot verify nonce binding even when the caller has one.

Evidence:
- `adapters/oidc/verifier.go:65-135`

Why this matters:
- OIDC Core requires `nonce` checking when a nonce was sent in the authorization request.
- For multi-audience tokens, OIDC Core says the client should validate `azp` when present. This implementation accepts any token whose `aud` contains the client ID and does not check trusted additional audiences.
- `jwt/v5` covers time-based validation, but that only handles generic JWT expiry/issued-at checks. It does not replace the OIDC-specific validation above.

Recommendation:
- Extend `Verify` to accept the expected nonce and validate it when the login flow uses one.
- Reject multi-audience tokens unless `azp` is present and matches the client ID, or unless the additional audiences are explicitly trusted.
- If the flow expects stricter replay protection, validate `auth_time`/max age as well.

## Overall assessment

The adapter is close functionally, but the trust boundary around discovery and transport is too loose for production use. The highest-risk items are:
- trusting discovered endpoints without issuer/origin binding,
- allowing non-HTTPS or arbitrary outbound targets,
- and keeping JWKS keys alive forever.

Those three issues are enough to let a compromised or poisoned identity provider path steer the client to attacker-controlled endpoints or keep old signing keys trusted after rotation.

## Source references

- OpenID Connect Discovery 1.0: https://openid.net/specs/openid-connect-discovery-1_0-errata1.html
- OpenID Connect Core 1.0: https://openid.net/specs/openid-connect-core-1_0.html
- RFC 8414: https://www.rfc-editor.org/rfc/rfc8414
