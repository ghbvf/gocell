# ADR: Refresh token delivered as an httpOnly cookie (BR-005 / #1278)

- Status: Accepted
- Date: 2026-06-06
- Issue: #1278 (BR-005); frontend decision source: ghbvf/gocell-web#12 (H2)

## Context

The gocell-web console stores access/refresh tokens only in memory (Pinia) to
keep long-lived credentials out of `localStorage`/`sessionStorage` (XSS
defense). The cost is that a cold start (refresh / new tab / browser restart)
clears memory and the session is lost. The only browser store that is both
*persistent across reloads* and *unreadable by JavaScript* is an **httpOnly
cookie**, which only the server can set via `Set-Cookie`. So accesscore must
deliver the refresh token as an httpOnly cookie, read it back on refresh, and
clear it on logout — **additively**, without breaking the existing `edge-bff`
client that uses the JSON body.

## Decision

1. **Set-Cookie on login (201) / refresh (200)**:
   `Set-Cookie: gocell_rt=<refreshToken>; HttpOnly; Secure; SameSite=Strict; Path=/api/v1/access/sessions; Max-Age=<refresh TTL>`.
2. **Refresh reads cookie-first, body-fallback**. The request schema's
   `refreshToken` becomes optional (`required: []`, `additionalProperties:false`
   kept); a cookie-only client still sends a JSON object body (minimally `{}`).
3. **Refresh rotation overwrites the cookie** with the new token (fresh Max-Age).
4. **Logout (204) clears the cookie** (`Max-Age=0`).
5. **CORS deferred** to a backlog issue (frontend uses a same-origin proxy /
   edge-bff in dev; the framework has no CORS middleware today).

### Mechanism — response-header injection for codegen handlers (first of its kind)

The codegen HTTP handler serializes a typed response envelope via
`visitXxxResponse → w.WriteHeader`; the slice adapter never receives the
`http.ResponseWriter`, and the codegen path has no escape hatch to set arbitrary
response headers (ADR 202605061500). A `Set-Cookie` must be written *before*
`WriteHeader` commits the status. So the cookie is injected by a **response-writer
wrapper** (`cells/accesscore/internal/httpcookie`) mounted via the existing
`cellmw.NewHeaderInjectMux`, driven by a **ctx directive** the adapter records
through `SetRefresh` / `ClearRefresh`. The wrapper intercepts `WriteHeader`, and
on a 2xx response emits the cookie. This is the response-side mirror of the
request-side `injectLoginTenant` ctx injection (the request-header-param gap is
tracked at #1494). The mechanism lives inside accesscore (its cookie name/path
are accesscore policy and it needs no `contractspec`); promote to `runtime/` only
if a second consumer appears.

`Max-Age` is a static `accesscore.DefaultRefreshMaxAge` (7d). This is correct,
not a shortcut: `refresh.memstore.Rotate` reissues the child token with
`now + Policy.MaxAge` on every rotation (sliding hard cap), so a static cookie
Max-Age stays aligned with the live token's hard expiry, and the cookie expires
in the browser at the same horizon the token dies server-side.

### Schema evolution

Relaxing `refreshToken` from required to optional is a pre-v1.0 direct v1
evolution (ADR 202605211200) — no v2 bump. `additionalProperties:false` is kept
(FMT-20). The embedded validator + typed `Request` are regenerated; the field
gains `omitempty`.

### The one intentional dual path (no-backward-compat exception)

GoCell's "no external callers, don't keep back-compat" premise does **not** hold
for these three contracts: `edge-bff` is a declared `endpoints.clients` consumer
that uses the body. The cookie-OR-body dual source is therefore an
issue-mandated capability (two client profiles), not a gratuitous shim. It is
declared explicitly here rather than buried in the code.

## Security model

| Threat | Mitigation | Status |
|--------|-----------|--------|
| XSS reading the long-lived refresh token | `HttpOnly` (JS cannot read the cookie) | ✅ |
| Token sent over plaintext | `Secure` (HTTPS-only; localhost is a secure context, so local dev works without a toggle) | ✅ |
| CSRF on the public refresh endpoint | `SameSite=Strict` (browser will not attach the cookie cross-site) + `Path` narrowed to the sessions subtree | ✅ (primary defense) |
| CSRF defense-in-depth (double-submit / Origin check) | Not added; SameSite=Strict + httpOnly judged sufficient (issue) | ⚠️ deferred (backlog if needed) |
| Cross-origin credentialed requests | CORS not implemented | ⚠️ deferred to backlog (same-origin/edge-bff in the interim) |
| Refresh reuse after the cookie is stale | Reuse → 401 cascade-revoke; the stale cookie is left untouched on 4xx (already useless; the 401 request is untrusted) | ✅ intentional |
| Refresh token leaking into logs/spans via Set-Cookie | The token is only passed to `http.SetCookie`, never to slog/spans; idempotency replay already strips `set-cookie`. No new leak surface (the token was already in the JSON body). | ✅ |

## AI-robust grading

The three security attributes (`HttpOnly`/`Secure`/`SameSite=Strict`) are the
entire point of the feature: silently dropping `HttpOnly` would turn the
long-lived refresh token into an XSS-readable credential — strictly worse than
the memory-only status quo. A unit test asserting the wire string is Soft. So:

- **`REFRESH-COOKIE-SECURE-ATTRS-01`** (`tools/archtest/refresh_cookie_secure_attrs_test.go`)
  — Medium. AST form-lock: every `http.Cookie` composite literal in the
  `httpcookie` package must set the three attributes; anti-vacuity (zero cookie
  literals ⇒ fail) + net/http alias blind-spot closure + RED fixture
  (`refreshcookiefixture`, `Secure:false` ⇒ exactly 1 diagnostic). **Hard ceiling
  (honest)**: the holder is stdlib `net/http.Cookie` with public bool fields, so
  the type system cannot make `Secure:false` unexpressible — a type-system Hard
  is unreachable (same family as the #851/#893 holder-seal ceilings). The
  directive-provenance side IS Hard: `directiveCtxKey` is unexported, so no
  cross-package code can forge a `SetRefresh` directive.

Full blind-spot inventory + reverse self-checks live in that archtest's package
godoc (single source per the AI-robust charter).

## Consequences

- Cookie-only clients must send a JSON object body (`{}`); a fully bodyless
  request still 400s (the handler requires a valid JSON body). Documented in the
  refresh `contract.yaml`.
- CORS and CSRF double-submit are deferred; a backlog issue tracks the
  credentialed CORS middleware (Access-Control-Allow-Credentials + Origin
  allowlist, never `*`).
- First response-header-via-middleware pattern in the codebase; if a second cell
  needs it, promote `httpcookie` to `runtime/`.
