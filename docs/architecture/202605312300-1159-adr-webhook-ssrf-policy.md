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

This PR ships the pure-computation SSRF guard (`kernel/webhook/ssrf.go`): a
`SafeDialContext` plus a redirect-deny `http.Client.CheckRedirect` and a
scheme/URL pre-flight. There is **no production consumer in this PR** — the
dispatcher that wires the guard into an `http.Transport` is PR-5. The policy is
decided here, in the same PR that implements it, rather than deferred: the dialer
code IS the decision, and the blocklist is security-critical.

## Decision

1. **`resolve → vet → dial-literal-IP`, single path.** `SafeDialContext` parses
   the address; if the host is an IP literal it vets it directly; otherwise it
   resolves the hostname via an injectable resolver, vets **every** returned IP
   against the blocklist (fail-closed: any blocked IP rejects the whole dial),
   then dials the **first vetted IP literal** with an inner `net.Dialer`. Because
   the connection target is the already-checked literal IP — never a re-resolved
   hostname — there is no DNS-rebinding (TOCTOU) window between check and connect.

2. **Deny-by-CIDR blocklist, explicit list as source of truth.** A 29-entry IPv4
   + IPv6 CIDR list (`ssrfBlockedCIDRStrings`) is the single source of truth,
   cross-checked against `kernel/webhook/testdata/webhook-ssrf-deny.yaml` by a
   drift-guard test. It is the IETF-reserved-range superset from
   doyensec/safeurl, covering RFC1918, loopback, link-local (incl.
   169.254.169.254 metadata), CGNAT (RFC6598), benchmarking, TEST-NET, multicast,
   reserved/broadcast, ULA, NAT64, 6to4, Teredo, documentation, and ORCHID
   ranges.

3. **IPv4-mapped IPv6 normalization is the SOLE mapped-IPv4 defense.** `vet`
   calls `normalizeIP` (`net.IP.To4()` dewrap) before the `Contains` check, so
   `::ffff:10.0.0.1` is checked as `10.0.0.1` (blocked by `10.0.0.0/8`) while
   `::ffff:8.8.8.8` dewraps to the public `8.8.8.8` and is allowed. The blocklist
   **deliberately omits `::ffff:0:0/96`**: with normalization it is unreachable
   dead weight, and without normalization it would over-block legitimate public
   IPv4-mapped addresses. Listing it would be a redundant/incorrect double path.

4. **Redirect deny-all.** `DenyRedirect` (an `http.Client.CheckRedirect`) refuses
   every 3xx. A redirect from a vetted target could otherwise bounce egress to an
   unvetted internal `Location`; webhook delivery is one-shot, so any redirect is
   an error.

5. **Scheme allowlist {http, https}.** `ValidateTargetURL` rejects every other
   scheme (`file`, `gopher`, `ftp`, …) and rejects a target whose host is an IP
   literal already in the blocklist (cheap pre-flight; hostnames are still vetted
   at dial time).

6. **`WithAllowLoopback` is dev/CI only.** It exempts ONLY loopback (so
   testcontainers can reach `127.0.0.1`); every other private/reserved range
   stays blocked even when it is set. It must never be enabled in production.

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
| A2 | `net.Dialer.DialContext` has a single callsite (`dial` in ssrf.go) — the sole vetted outbound | downstream **Hard** |
| A3 | `http.DefaultClient`/`DefaultTransport`/`Get`/`Post`/`PostForm`/`Head` banned in kernel/webhook | downstream **Hard** |

**Upstream is Medium = a permanent Go-language ceiling.** The holder axis —
"only `NewSafeDialer` may produce a dialer that a struct holds" — is
inexpressible in Go's type system; package visibility constrains implementers,
not who may declare a field of a type. This is the same permanent ceiling as
`SPAN-SETATTR-HOLDER-SEAL` (#851) and `HEALTHZ-HOLDER-SEAL` (#893). Per
ai-robust.md §Funnel 双向锁评级, the Medium-upstream axis carries a tracking
issue: **won't-do gh #1375**, named in the funnel godoc.

**PR-4 vs PR-5 split.** The implementation plan's PR-4 row called for a
`Dispatcher.client` field-type downstream lock. `Dispatcher` does not exist until
PR-5 — a field scan now would pass vacuously, which is worse than absent. The
`Dispatcher.client` typed-field lock (a single-sanctioned-holder Hard: a typed
field whose only constructor wraps `NewSafeDialer`) is therefore deferred to
PR-5, where it is the dispatcher-specific upstream tightening that complements
this PR's package-level callsite ban.

**Documented blind spot (B1).** A hand-rolled `http.Transport` whose
`DialContext` is not the SafeDialContext, invoked via `Transport.RoundTrip`,
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
| Resolver failure / empty answer fail-open | both return `ErrWebhookSSRFBlocked` (fail-closed) | ✅ |
| Blocked-IP value leaking to wire client | IP only in `WithInternal` (server slog); 403 wire body carries no IP | ✅ |
| CIDR list drift (code vs fixture) | `ssrf_fixtures_test.go` bidirectional set-equality drift guard | ✅ |
| Un-vetted egress added later in kernel/webhook | WEBHOOK-SSRF-GUARD-01 A1/A2/A3 callsite bans (downstream Hard) | ✅ in-package; ⚠️ `Transport.RoundTrip` (B1) is a documented static blind spot, closed by the PR-5 `Client.Do` funnel |

No row depends on `WithAllowLoopback`, which is dev/CI-only and exempts loopback
alone.

## Consequences

PR-5's dispatcher consumes `NewSafeDialer` as the only outbound dialer
(`http.Transport{DialContext: safeDial}`), sets `CheckRedirect: DenyRedirect`,
and calls `ValidateTargetURL` before issuing a request; the A1/A2/A3 bans mean it
cannot wire an un-vetted egress path without tripping CI. The blocklist evolves
by editing `ssrfBlockedCIDRStrings` **and** the fixture in the same change (the
drift guard enforces this). The upstream holder-axis Hard-ization remains a
won't-do permanent ceiling (#1375); PR-5 adds the dispatcher-specific
`Dispatcher.client` typed-field lock as the practical upstream tightening.

## References

- stripe/smokescreen `pkg/smokescreen/smokescreen.go` — `resolveTCPAddr` / `selectTargetAddr` / `classifyAddr` / `dialContext` (resolve→vet→dial-literal)
- stealthrocket/netjail `security.go` `Rules.DialFunc` (resolve→vet→dial-literal; Convoy adopts)
- doyensec/safeurl `ip.go` `privateNetworks` (CIDR superset source) + `client.go` (Control mode — deliberately not used)
- mccutchen/safedialer — Control-mode minimal reference
- Convoy SSRF guide: https://www.getconvoy.io/docs/webhook-guides/tackling-ssrf
- ADR `202605291200-adr-webhook-signing-algorithm.md` (sibling webhook ADR; HTTP status mapping, secret-leak defense)
- ADR `202605051730-adr-errcode-message-pii-safety.md` (Message/Details/Internal redaction; blocked IP → Internal channel)
- ai-robust.md §Funnel 双向锁评级; gh #1375 (upstream holder-axis won't-do), #851 / #893 (Go-language-ceiling precedents)
