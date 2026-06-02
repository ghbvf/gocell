// Package contractbuild is the sole sanctioned runtime-side construction funnel
// for framework-owned kernel/contractspec.ContractSpec values.
//
// # Usage
//
// Framework HTTP infra endpoint (health probe, devtools, etc.):
//
//	spec := contractbuild.NewFrameworkHTTP("http.framework.<subsystem>.<endpoint>.v1", "GET", "/path")
//
// (the ID MUST start with "http.framework." — a missing prefix panics at
// startup.) Event-tracing projection of a validated outbox.Subscription:
//
//	spec, err := contractbuild.NewEventDerivation(sub) // sub passes sub.Validate()
//
// Outbound webhook dispatcher subscription spec from a validated DispatchSpec:
//
//	spec, err := contractbuild.NewWebhookDispatch(dispatchSpec) // dispatchSpec passes dispatchSpec.Validate()
//
// # Why this package exists (compiler-Hard upstream)
//
// ContractSpec values come from exactly two homes:
//
//   - generated/contracts/**/spec_gen.go — business contracts (contractgen
//     codegen output).
//   - this package — framework-owned HTTP infra (health probes, devtools
//     catalog) and event-tracing derivations.
//
// Hand-written ContractSpec{…} composite literals are forbidden under cells/,
// examples/**/cells/, and runtime/ by archtest NO-MANUAL-CONTRACTSPEC-LITERAL-01
// (downstream Hard). The two funnels here — NewFrameworkHTTP and
// NewEventDerivation — are the only legitimate runtime-side construction paths.
//
// Location: this package lives under runtime/internal/, so the Go compiler
// itself refuses imports from outside the runtime/ subtree (cells/, examples/,
// kernel/, cmd/, adapters/, tools/, tests/). Business code therefore *cannot*
// construct a framework ContractSpec — the violation is unrepresentable, not
// merely archtest-detected.
//
// Before issue #1038 the funnels lived in kernel/contractspec. NewFrameworkHTTP
// had an open caller (any package could call it; only the FrameworkHTTPIDPrefix
// panic gated content), and NewEventDerivation relied on a single-file
// path-string allowlist in archtest NO-MANUAL-CONTRACTSPEC-LITERAL-01 (upstream
// Medium). A token-in-kernel design could not reach Hard: the callers live in
// runtime/ (a different package from kernel/contractspec), so kernel would have
// to export a mint function reachable by everyone — the permanent Go ceiling
// recorded at #851 / #893 / #1282 ("Go package visibility cannot express 'only
// these packages may call this exported symbol'").
//
// The internal/ move resolves it because *all* callers (runtime/bootstrap,
// runtime/http/devtools, runtime/eventrouter) sit under runtime/. This is the
// same Medium→Hard upgrade issue #638 applied to runtime/internal/authtest;
// see ai-robust.md §Hard 范本目录 → "internal/ wrap 包".
//
// # AI-robust grading (single source; not duplicated in rule docs)
//
// The "upstream Hard" claim is precisely scoped to the threat class —
// business code constructing framework specs — which is exactly the set of
// non-runtime packages (cells/, examples/, cmd/, adapters/, kernel/). Two
// orthogonal vectors:
//
//   - Non-runtime construction: Hard. Those packages can neither import this
//     package (Go internal/ rule — compiler Hard) nor write a
//     contractspec.ContractSpec{…} literal: NO-MANUAL-CONTRACTSPEC-LITERAL-01
//     scans EVERY production tree that can import kernel/contractspec — cells/,
//     examples/, runtime/, kernel/, cmd/, adapters/, cellmodules/ (#1038 review
//     C1/F2 widened the scan beyond the original cells/+examples-cells/+runtime/
//     so this claim is repo-wide true, not just for cells/). Both forms are
//     unrepresentable. (The literal-ban is archtest-enforced, not a compiler
//     gate; per the established grading it is the funnel's downstream Hard.)
//   - Within-runtime construction: open by design for the funnel CALL (any
//     runtime/ framework infra may call NewFrameworkHTTP / NewEventDerivation —
//     these are framework-owned infra, not business contracts), while a raw
//     contractspec.ContractSpec{…} literal inside runtime/ is still caught by
//     NO-MANUAL-CONTRACTSPEC-LITERAL-01. There is no within-runtime caller
//     allowlist to Hard-ify (it was retired, see below) — the openness is
//     intentional, not a Medium funnel awaiting upgrade, so no tracking issue.
//
// Per-funnel content invariants:
//
//   - NewFrameworkHTTP content: the ID field is Hard (frameworkHTTPIDPrefix
//     A-class panic asserts framework ownership). Method/Path are NOT validated
//     here — their structural validity is enforced downstream at route
//     registration (runtime/auth.Route.validateContractShape via auth.Mount).
//     The panic fires at package-initialization time (call sites are
//     package-level var assignments with static-literal IDs), so it is not
//     recoverable by the HTTP middleware layer — a malformed prefix fails the
//     process at startup with a Go stack trace, by design.
//   - NewEventDerivation content + provenance: Hard. The parameter is a typed
//     outbox.Subscription (not loose primitives), and the funnel runs
//     sub.Validate() before deriving — so a runtime/ caller cannot mint an event
//     spec from arbitrary strings; it must supply a fully-populated, valid
//     subscription. The derived spec additionally passes ContractSpec.Validate()
//     (defense in depth). The former single-file ("only eventrouter") allowlist +
//     drift guard are RETIRED (the typed-parameter provenance gate replaced
//     them — #1038 / #1445, the latter closed by this signature). The primitive
//     signature was a vestige of the old kernel/contractspec placement (which
//     could not import kernel/outbox); runtime/ placement makes the typed param
//     natural.
//   - NewWebhookDispatch content + provenance: Hard/Hard. The parameter is a typed
//     webhook.DispatchSpec (not loose primitives), and the funnel runs
//     spec.Validate() before deriving — so a runtime/ caller cannot mint a webhook
//     dispatch subscription spec from arbitrary strings; it must supply a
//     fully-populated, valid dispatch spec (ContractID/SourceID/CellID all
//     required). The derived spec additionally passes ContractSpec.Validate()
//     (defense in depth). Upstream Hard via runtime/internal/ placement (same
//     guarantee as NewEventDerivation); downstream Hard via
//     NO-MANUAL-CONTRACTSPEC-LITERAL-01. Grading: Hard/Hard, same form as
//     NewEventDerivation.
//   - ContractSpec{…} literal ban: downstream Hard (unchanged,
//     NO-MANUAL-CONTRACTSPEC-LITERAL-01). runtime/internal/contractbuild is the
//     sanctioned funnel home and is excluded from that scan, analogous to the
//     former kernel/contractspec/** exclusion.
//
// ref: runtime/internal/authtest (#638 internal/ Medium→Hard precedent);
// docs/reviews/202605181109-042-archtest-six-agent-audit.md §3b/§4 row 9.
package contractbuild
