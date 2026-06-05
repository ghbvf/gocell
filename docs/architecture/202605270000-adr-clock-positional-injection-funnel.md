# ADR: clock positional-injection funnel (CLOCK-POSITIONAL-INJECTION-01)

> Status: Accepted
> Date: 2026-05-27
> Supersedes: `docs/architecture/202605021500-adr-kernel-clock-injection.md` (§Injection convention + the "WithClock Option" / "required-no-fallback" rejected-alternative bullets are reversed here)
> ref: issue #1053 (clock funnel 总收口 epic); absorbs #682 / #619 / #1022; adjudicates #883

## Context

The `kernel/clock` package, its `clock.Real()` sole production constructor, the
`clockmock.New(...)` fake, and the type-driven `PROD-CLOCK-INJECTION-01` archtest
(which bans `time.Now/Since/Until/NewTimer/NewTicker/After/AfterFunc/Tick/Sleep`
in production via `go/types` resolution) **already existed** before this change.
Issue #1053's framing ("clock injection is Soft / comment-marker, no sealed
interface") was largely stale.

The genuine residual gap (#682's actual ask) was the **injection vector**, which
had two *omittable* forms:

- **functional option** — `NewService(opts...)` where `WithClock(clk)` is one of
  the options; `NewService()` with no option compiles fine.
- **struct field** — `New(cfg Config)` where `cfg.Clock` is a field; `New(Config{})`
  compiles fine.

Both forms let a caller omit the clock entirely and only fail at runtime via the
`clock.MustHaveClock` panic. The archtest `CLOCK-INJECTION-TEST-CALLSITE-01`
covered the option form at *test* callsites (Medium), but the struct-field form
was uncovered, and neither was compile-enforced.

## Decision

Clock injection becomes a **mandatory positional parameter** on every
clock-requiring constructor. Omitting a clock is now a **compile error**.

1. Every constructor that requires a clock takes `clk clock.Clock` as a
   positional parameter (first parameter, or immediately after `ctx
   context.Context` when present). Constructors that already took clock
   positionally are left in place (no reordering churn).
2. The body guards with `clock.MustHaveClock(clk, "<context>")`, checking the
   **parameter itself** (not a struct field).
3. All `WithClock(clk clock.Clock) Option` functions are **deleted**.
4. The `Clock clock.Clock` field is **removed** from every input Config/Options
   struct (e.g. `oidc.Config`, `assembly.Config`, `websocket.HubConfig`, the
   relay/registry/lifecycle configs). The composition-root holder
   `cmd/corebundle.SharedDeps.Clock` (the single source where `clock.Real()`
   enters and is threaded) and internal storage fields are unaffected.

`clock.MustHaveClock` is **retained** as the typed-nil guard: a positional
`clk clock.Clock` can still be passed `nil`/typed-nil, which `MustHaveClock`
rejects with a programmer-error panic. Presence (omission) is the compiler's
job; non-nil is `MustHaveClock`'s job. These are orthogonal axes.

### AI-HARD funnel (bidirectional)

- **Upstream Hard = the Go compiler.** A mandatory positional `clk clock.Clock`
  parameter makes "construct without a clock" type-system-unrepresentable. There
  is no option bag or struct literal that can elide it.
- **Downstream Hard = `CLOCK-POSITIONAL-INJECTION-01`** (new archtest,
  `tools/archtest/clock_invariants_test.go`). Two sub-checks over production code:
  (A) every `clock.MustHaveClock` first argument must resolve (via `go/types`) to
  a *parameter* of the enclosing function — selector args like `cfg.Clock` /
  `s.clk` are rejected; (B) no exported `func WithClock` may exist. Together these
  make "reintroduce an omittable injection form" unrepresentable, so the upstream
  compiler-enforced presence cannot silently regress. Blind-spot self-checks +
  RED fixtures live in the archtest godoc.

This retires `CLOCK-INJECTION-TEST-CALLSITE-01` and `CLOCK-INJECTION-PROD-CALLSITE-01`
(both subsumed — the compiler now enforces presence at every callsite, prod and
test, so the WithClock-presence scans are vacuous).

### #619 — control-plane clock (sealed type + host→method allowlist)

The control-plane scheduling primitives (startup probe / delayed requeue / leader-
lease renew) must use real wall-clock time — a frozen fake would deadlock
`Start()`. The original carve-out used a `//archtest:allow:clock-injection:control-plane`
comment-marker (AI-abusable Soft/Medium).

> **Amendment PR-A8 #1169**: the original carve-out lived in `runtime/command/lifecycle.go`
> (the now-deleted `SweeperLifecycle`). It moved to `kernel/reconcile/` when the sweep
> migrated to a generic `reconcile.Loop`. Read the current-tense description below as
> host = `kernel/reconcile/`.

The comment-marker is replaced by a package-private sealed type `controlPlaneClock
struct{}`, gated by `PROD-CLOCK-INJECTION-01` two ways (BOTH required):

1. **Receiver-type confinement** — `time.NewTimer` / `time.NewTicker` may only be
   called from methods whose receiver type is the package-private `controlPlaneClock`
   (unexported → no external constructor; the gate checks the receiver *type*, not
   the method name).
2. **Host→method allowlist** — `controlPlaneClockCarveOut` maps each sanctioned host
   prefix to the exact `{controlPlaneClock method → sanctioned time.* callee}` set.
   The single host is `kernel/reconcile/`, with methods `newProbeTimer`→`NewTimer`,
   `newRequeueTimer`→`NewTimer`, `newRenewTicker`→`NewTicker`. (The map is NOT
   deleted — it is the host-scoping half of the funnel; only the comment-marker and
   the old `CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01` rule are gone.) Adding a
   `controlPlaneClock` method without a map entry, or any other `time.*` call inside
   such a method (or a closure within it), is flagged.

**AI-robust grade: Medium (permanent ceiling).** `time.NewTicker` / `time.NewTimer`
are stdlib free functions and cannot be made uncallable in Go — same ceiling as
`SPAN-SETATTR-REDACT-01`'s package-internal axis. The net gain over the prior form
is the elimination of the comment-marker mechanism (a new `time.*` site now
requires adding a method to the sealed type — a deliberate, reviewable change). No
gh issue is opened to "Hard-ify" because the ceiling is structural, not a backlog
item.

## Master adjudication table (#1053 总收口)

| Issue | Decision | Landing |
|-------|----------|---------|
| #682 (P1) — struct-field clock injection not statically covered | **ABSORBED** | resolved structurally: the struct-field form is deleted; positional param makes presence compile-enforced; form-lock locks it. |
| #619 (P3) — control-plane clock comment-marker → Hard | **ABSORBED** | sealed `controlPlaneClock` + `controlPlaneClockCarveOut` host→method allowlist (host `kernel/reconcile/` since PR-A8 #1169; Medium ceiling, documented). |
| #1022 (P3) — FMT-23 boundary test wall-clock flake | **ABSORBED** | `TestFMT23_DeprecatedCleanup_BoundaryCheck` + `TestContractDeprecatedCleanup01` anchored to a fixed `clockmock.New` instant. (No `fmt23DeprecationDaysRemaining` metric exists — the issue's reference was never built.) |
| #883 (P3) — fold MustHaveClock into required-dep funnel? | **Option A confirmed** | `MustHaveClock` stays a panic-style guard (programmer-error, caller-controlled construct-time invariant); not folded into the `gocell:"required"` errcode funnel. Already ratified in `go-standards.md`; the positional param now makes *presence* compile-enforced, leaving `MustHaveClock` solely for the typed-nil axis. |

### Declined: declarative downstream lint ban (issue scope #2)

Issue #1053 listed "`.golangci.yml` depguard 全包禁 time.Now/Sleep/NewTimer".
**Declined.** `depguard` only bans whole packages, not specific functions
(`time.Duration`/`time.Parse` are legitimately used); `forbidigo` (the correct
function-level tool) would duplicate the existing type-driven
`PROD-CLOCK-INJECTION-01` archtest and create a second exemption list that drifts.
Banning a stdlib free function (`time.Now`) is structurally Medium-ceiling
regardless of carrier. The single nightly type-driven archtest remains the source
of truth; no PR-time/IDE duplicate is added.

### Scope boundary: examples/

`examples/` are outside `prodscan` scope (`tools/internal/prodscan/patterns.go`),
so `CLOCK-POSITIONAL-INJECTION-01` does not gate them. Their `WithClock` options
and `Config.Clock` fields were nonetheless converted to positional params for
whole-tree consistency; the lack of a gate there is the framework-wide
examples-exclusion convention, not a silent gap.

## Consequences

- Positive: clock omission is a compile error everywhere (prod + test); a single
  injection form (positional param) replaces three (option / struct-field /
  positional); ~14 `WithClock` options + ~13 input `Config.Clock` fields + the
  `CLOCK-INJECTION-TEST-CALLSITE-01` / `-PROD-CALLSITE-01` archtests + the
  control-plane comment-marker + its allowlist map are deleted — net reduction of
  governance surface.
- Negative: large mechanical sweep (~1000 callsites: composition roots pass
  `clock.Real()`, tests pass `clockmock.New(...)`). No backwards-compat shim
  (project has no external consumers).

## References

- `tools/archtest/clock_invariants_test.go` — `CLOCK-POSITIONAL-INJECTION-01`,
  `PROD-CLOCK-INJECTION-01` (receiver-type confinement), `KERNEL-CLOCK-LEAF-FALLBACK-01`
- `kernel/reconcile/` — `controlPlaneClock` sealed type + `controlPlaneClockCarveOut` host→method allowlist (was `runtime/command/lifecycle.go` pre-PR-A8 #1169)
- `kernel/clock/guard.go` — `MustHaveClock`
- `.claude/rules/gocell/ai-robust.md` §Hard 范本目录 ("sealed construction")
- benbjohnson/clock, uber-go/fx — required-dep-not-in-optional-options reference
- supersedes `docs/architecture/202605021500-adr-kernel-clock-injection.md`
