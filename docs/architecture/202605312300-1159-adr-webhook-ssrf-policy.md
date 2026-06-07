# ADR: Webhook SSRF policy — resolve→vet→dial-literal, deny-by-CIDR, redirect deny

- Status: Accepted
- Date: 2026-05-31
- Context: KERNEL-WEBHOOK-01 PR-4 (#1159)
- Supersedes / amends: none (greenfield)

## Context

The outbound dispatcher (PR-5) will POST webhook deliveries to **operator-supplied
target URLs** — a classic Server-Side Request Forgery (SSRF) surface: an attacker
who can register or influence a target URL points it at `169.254.169.254` (cloud
metadata), an internal service, or loopback, turning the dispatcher into a
confused-deputy proxy into the trust boundary.

This PR ships the pure-computation SSRF guard (`kernel/webhook/ssrf.go`) as a
**single-source policy object**, `SafePolicy`, whose three methods —
`DialContext` (the vetted dialer), `DenyRedirect` (an `http.Client.CheckRedirect`)
and `ValidateTargetURL` (the scheme/URL pre-flight) — share one config. Bundling
them means a policy knob (e.g. `WithAllowLoopback`) applies **identically** to the
pre-flight and the dial, with no divergent per-surface vetting path. There is **no
production consumer in this PR** — the dispatcher that holds a `*SafePolicy` and
wires its methods into an `*http.Client` is PR-5. The policy is decided here, in
the same PR that implements it, rather than deferred: the dialer code IS the
decision, and the blocklist is security-critical.

## Decision

1. **`resolve → vet → dial-literal-IP`, single path.** `SafePolicy.DialContext`
   parses the address; if the host is an IP literal it vets it directly; otherwise it
   resolves the hostname via an injectable resolver, vets **every** returned IP
   against the blocklist (fail-closed: any blocked IP rejects the whole dial),
   then dials the **first vetted IP literal** with an inner `net.Dialer`. Because
   the connection target is the already-checked literal IP — never a re-resolved
   hostname — there is no DNS-rebinding (TOCTOU) window between check and connect.

2. **Deny-by-CIDR blocklist, explicit list as source of truth.** A 30-entry IPv4
   + IPv6 CIDR list (`ssrfBlockedCIDRStrings`) is the single source of truth,
   cross-checked against `kernel/webhook/testdata/webhook-ssrf-deny.yaml` by a
   drift-guard test. It is the IETF-reserved-range superset from
   doyensec/safeurl, covering RFC1918, loopback, link-local (incl.
   169.254.169.254 metadata), CGNAT (RFC6598, incl. Alibaba ECS
   `100.100.100.200`), benchmarking, TEST-NET, multicast, reserved/broadcast,
   ULA (incl. AWS IMDSv6 `fd00:ec2::254`), NAT64 (RFC6052 well-known
   `64:ff9b::/96` + RFC8215 local-use `64:ff9b:1::/48`, which would otherwise
   embed arbitrary IPv4 incl. private), 6to4, Teredo, documentation, and ORCHID
   ranges.

3. **IPv4-mapped IPv6 normalization is the SOLE mapped-IPv4 defense.** `vet`
   calls `normalizeIP` (`net.IP.To4()` dewrap) before the `Contains` check, so
   `::ffff:10.0.0.1` is checked as `10.0.0.1` (blocked by `10.0.0.0/8`) while
   `::ffff:8.8.8.8` dewraps to the public `8.8.8.8` and is allowed. The blocklist
   **deliberately omits `::ffff:0:0/96`**: with normalization it is unreachable
   dead weight, and without normalization it would over-block legitimate public
   IPv4-mapped addresses. Listing it would be a redundant/incorrect double path.

4. **Redirect deny-all.** `SafePolicy.DenyRedirect` (an `http.Client.CheckRedirect`)
   refuses every 3xx. A redirect from a vetted target could otherwise bounce egress
   to an unvetted internal `Location`; webhook delivery is one-shot, so any redirect
   is an error.

5. **Scheme allowlist {http, https} + fail-closed pre-flight.**
   `SafePolicy.ValidateTargetURL` rejects every other scheme (`file`, `gopher`,
   `ftp`, …), rejects an **empty host** (an un-vettable target — e.g. `http:///x` —
   fails closed rather than passing silently), and rejects a target whose host is
   an IP literal blocked by the policy. The IP-literal check **reuses the same
   `vet`** as the dial path, so the pre-flight and the dial cannot diverge (a
   literal the dialer would accept is never falsely rejected at pre-flight, and
   vice versa). Hostnames are still vetted at dial time (resolving at pre-flight
   would create a TOCTOU window).

6. **`WithAllowLoopback` is dev/CI only — and policy-coherent.** It exempts ONLY
   loopback (so testcontainers can reach `127.0.0.1`); every other private/reserved
   range stays blocked even when it is set. Because the pre-flight and the dial
   share one config and one `vet`, the exemption applies to **both** surfaces: with
   it set, `ValidateTargetURL("http://127.0.0.1/")` passes exactly as the dial
   does. It must never be enabled in production.

7. **All errors are `KindPermissionDenied` → HTTP 403** via the existing
   `errcode.ErrWebhookSSRFBlocked` sentinel (no new sentinel). The blocked IP is
   carried in `errcode.WithInternal` (server-side slog only), never on the wire.

## Rationale / alternatives considered

- **`resolve→vet→dial-literal` vs `net.Dialer.Control`.** The Control-callback
  approach (doyensec/safeurl, mccutchen/safedialer) vets the IP the runtime
  resolver picked, inside a `syscall.RawConn` callback — simpler, but the DNS
  resolution still happens inside `net.Dialer` where a test cannot substitute a
  fake answer without monkeypatching, and it leaves a theoretical rebinding
  window on connection reuse. The resolve-then-dial-literal model
  (stripe/smokescreen `dialContext`, stealthrocket/netjail `Rules.DialFunc`,
  which Convoy adopts) is the industry-strictest, rebinding-proof form and puts
  resolution behind an interface we own — exactly what makes the DNS-rebinding
  test clean. Per the project's no-double-path rule we ship **only** this design;
  there is no Control fallback alongside it.
- **TLS/SNI is unaffected.** `http.Transport` derives `ServerName` (SNI + cert
  verification host) from the request URL host *above* the dialer, independent of
  the literal IP the dialer connects to. PR-5 wires `Transport{DialContext: safeDial}`
  without a custom `DialTLSContext`.
- **Explicit CIDR list vs `net.IP.IsPrivate()`.** `IsPrivate`/`IsGlobalUnicast`
  miss SSRF-critical ranges (CGNAT 100.64/10, IETF protocol 192.0.0/24, 6to4
  relay 192.88.99/24, NAT64) and bundle policy that cannot be fixture-diffed. An
  explicit declared list is auditable, version-independent, and is the only form
  the drift-guard cross-check can operate on. (smokescreen leans partly on
  stdlib + a few explicit IPv6 ranges; we enumerate fully for auditability.)
- **Deny-all redirect vs re-check-on-redirect.** netjail/Convoy re-apply the
  rules on each redirect's dial; we deny outright. Webhook endpoints should not
  redirect, and deny-all is simpler and matches Stripe/GitHub webhook delivery.

## AI-robust enforcement: WEBHOOK-SSRF-GUARD-01

Carrier: `tools/archtest/webhook_ssrf_guard_test.go`. Symbol inventory and
blind-spot list live in that test's package godoc (per ai-robust.md "落地实例与
符号清单活在代码 godoc"); this ADR records only the ratings.

| Sub-rule | Guards | Rating |
|----------|--------|--------|
| A1 | `net.Dial`/`DialTCP`/`DialUDP`/`DialIP`/`DialUnix`/`DialTimeout` package funcs banned in kernel/webhook | downstream **Hard** |
| A2 | `net.Dialer.DialContext` callable only from `(*SafePolicy).DialContext` — allowance bound to the go/types **FullName method identity** (not a func name or filename), the sole vetted outbound | downstream **Hard** |
| A3 | `http.DefaultClient`/`DefaultTransport`/`Get`/`Post`/`PostForm`/`Head` banned in kernel/webhook | downstream **Hard** |

A2 legitimately covers **two** callsites inside `(*SafePolicy).DialContext` (the
IP-literal and the resolved-hostname dial); the identity binding allows exactly
that method and nothing else. A rename of the method flips every `DialContext`
callsite to a violation (fail-closed), forcing the author to update the
sanctioned-identity const in the same change. The reverse fixture
(`testdata/webhook_ssrf_violate`) exercises the full A1 banned set, a raw
`net.Dialer{}.DialContext` (A2), and **every** A3 global + convenience callee
(`Get`/`Post`/`PostForm`/`Head`), so dropping any one banned entry fails the
blind-spot self-test instead of passing on the others.

**Upstream is Medium = a permanent Go-language ceiling.** The holder axis —
"only a `*SafePolicy` that a dispatcher holds may produce egress" — is
inexpressible in Go's type system; package visibility constrains implementers,
not who may declare a field of a type. This is the same permanent ceiling as
`SPAN-SETATTR-HOLDER-SEAL` (#851) and `HEALTHZ-HOLDER-SEAL` (#893). Per
ai-robust.md §Funnel 双向锁评级, the Medium-upstream axis carries a tracking
issue: **won't-do gh #1375**, named in the funnel godoc.

**PR-4 vs PR-5 split.** The implementation plan's PR-4 row called for a
`Dispatcher.client` field-type downstream lock. `Dispatcher` does not exist until
PR-5 — a field scan now would pass vacuously, which is worse than absent. The
single-source `SafePolicy` makes that future lock **cleaner**: PR-5's dispatcher
holds one `*SafePolicy` field (not three free funcs), so the single-sanctioned-
holder Hard is a typed field whose only constructor is `NewSafePolicy`. It is
therefore deferred to PR-5, where it is the dispatcher-specific upstream
tightening that complements this PR's package-level callsite ban.

**Documented blind spot (B1).** A hand-rolled `http.Transport` whose
`DialContext` is not `SafePolicy.DialContext`, invoked via `Transport.RoundTrip`,
bypasses the funnel. `RoundTrip` has no resolvable package-callee form
distinguishing a safe vs unsafe transport; catching it needs type-level dataflow
we do not have. Bounded response: the realistic egress path is `http.Client.Do`
over the SSRF transport, locked when the dispatcher lands in PR-5.

## Threat model

| Threat | Mitigation | Status |
|--------|-----------|--------|
| Target → cloud metadata (169.254.169.254) | `169.254.0.0/16` in blocklist; vetted at dial time | ✅ |
| Target → RFC1918 / loopback / ULA internal host | full CIDR blocklist; `vet` rejects | ✅ |
| DNS rebinding (public name → private IP) | resolve→vet→dial-literal; no re-resolution between check and connect; all resolved IPs vetted | ✅ |
| IPv4-mapped IPv6 evasion (`::ffff:10.0.0.1`) | `normalizeIP` To4 dewrap before `Contains` | ✅ |
| Multi-answer split (one public + one private A record) | every resolved IP vetted; any blocked IP fails the whole dial (fail-closed) | ✅ |
| Redirect to internal `Location` after passing vet | `DenyRedirect` refuses all 3xx | ✅ |
| Non-HTTP scheme abuse (`file://`, `gopher://`) | `ValidateTargetURL` scheme allowlist {http, https} | ✅ |
| Un-vettable empty-host URL (`http:///x`) slipping past pre-flight | `ValidateTargetURL` rejects empty host (fail-closed) | ✅ |
| Pre-flight ↔ dial policy divergence (loopback exempt at dial but rejected at pre-flight, or a blocklist drift between the two) | both share one `vet` on one config — single source, structurally cannot diverge | ✅ |
| Resolver failure / empty answer fail-open | both return `ErrWebhookSSRFBlocked` (fail-closed) | ✅ |
| Blocked-IP value leaking to wire client | IP only in `WithInternal` (server slog); 403 wire body carries no IP | ✅ |
| CIDR list drift (code vs fixture) | `ssrf_fixtures_test.go` bidirectional set-equality drift guard | ✅ |
| Un-vetted egress added later in kernel/webhook | WEBHOOK-SSRF-GUARD-01 A1/A2/A3 callsite bans (downstream Hard) | ✅ in-package; ⚠️ `Transport.RoundTrip` (B1) is a documented static blind spot, closed by the PR-5 `Client.Do` funnel |

No row depends on `WithAllowLoopback`, which is dev/CI-only and exempts loopback
alone — uniformly across the pre-flight and the dial (one shared `vet`).

## Consequences

PR-5's dispatcher holds one `*SafePolicy` and wires its three methods: the
`DialContext` as the only outbound dialer, `DenyRedirect` as `CheckRedirect`, and
`ValidateTargetURL` before issuing a request; the A1/A2/A3 bans mean it cannot
wire an un-vetted egress path without tripping CI. The intended wiring:

```go
// PR-5 dispatcher (canonical wiring).
policy := webhook.NewSafePolicy()                          // one policy, shared config
t := &http.Transport{DialContext: policy.DialContext}      // ONLY DialContext;
// do NOT set DialTLSContext — http.Transport derives the TLS ServerName (SNI +
// cert host) from the request URL host above the dialer, so dialing a vetted
// literal IP does not break TLS. A custom DialTLSContext would bypass the vet.
client := &http.Client{Transport: t, CheckRedirect: policy.DenyRedirect}
if err := policy.ValidateTargetURL(targetURL); err != nil { /* reject before send */ }
```

The blocklist evolves by editing `ssrfBlockedCIDRStrings` **and** the fixture in
the same change (the drift guard enforces this).

**Observability.** PR-4 surfaces an SSRF rejection only via
`errcode.ErrWebhookSSRFBlocked` + server-side `slog` Internal attributes — there
is no metric, because `kernel/webhook` is pure-computation and must not depend on
an instrumentation adapter. **Every** reject path carries a stable `reason`
enum suitable for a future `{reason}` metric label —
`malformed_address` / `resolution_failed` / `no_addresses` / `ssrf_blocked`
(dial); `unparseable` / `empty_host` / `scheme_not_allowed` / `ssrf_blocked`
(pre-flight); `redirect_denied` (redirect) — alongside the relevant target field
(`address` / `host` / `ip` / `scheme` / `target_host` / `from_url`), correlated by
`request_id`. PR-5's wiring layer is the place to increment a counter (e.g.
`webhook_ssrf_blocked_total{reason}`) on
`errors.As(err, &ec) && ec.Code == ErrWebhookSSRFBlocked` at the
`Transport`/`CheckRedirect` error boundary.

**`WithAllowLoopback` production guard.** The option is dev/CI-only.
**Amendment 2026-06-07 (#1378):** the prior Soft godoc-only constraint is now a
**Medium archtest** `WEBHOOK-ALLOW-LOOPBACK-PROD-BAN-01` — any reference to
`webhook.WithAllowLoopback` in a non-`_test.go` production file fails CI (today
vacuous green: every caller is a kernel/webhook same-package test). The wiring
layer (corebundle / assembly) must not pass it. The stronger Hard form —
unexporting to `withAllowLoopback` so an external reference is a Go compile error
(achievable today, all callers being same-package tests) — is deliberately
deferred to preserve the dev escape hatch / future cross-package integration
tests, tracked at gh #1730 and named in the archtest godoc per the AI-robust
§Funnel 双向锁评级 requirement. The SSRF holder-axis Hard-ization (the dispatcher
egress holder) remains the separate won't-do permanent ceiling #1375.

## References

- stripe/smokescreen `pkg/smokescreen/smokescreen.go` — `resolveTCPAddr` / `selectTargetAddr` / `classifyAddr` / `dialContext` (resolve→vet→dial-literal)
- stealthrocket/netjail `security.go` `Rules.DialFunc` (resolve→vet→dial-literal; Convoy adopts)
- doyensec/safeurl `ip.go` `privateNetworks` (CIDR superset source) + `client.go` (Control mode — deliberately not used)
- mccutchen/safedialer — Control-mode minimal reference
- Convoy SSRF guide: https://www.getconvoy.io/docs/webhook-guides/tackling-ssrf
- ADR `202605291200-adr-webhook-signing-algorithm.md` (sibling webhook ADR; HTTP status mapping, secret-leak defense)
- ADR `202605051730-adr-errcode-message-pii-safety.md` (Message/Details/Internal redaction; blocked IP → Internal channel)
- ai-robust.md §Funnel 双向锁评级; gh #1375 (upstream holder-axis won't-do), #851 / #893 (Go-language-ceiling precedents)
