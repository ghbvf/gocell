// Package contractbuild is the sole sanctioned runtime-side construction funnel
// for framework-owned kernel/contractspec.ContractSpec values.
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
//   - NewFrameworkHTTP upstream: open → Hard. The compiler refuses non-runtime
//     imports of this package; within runtime/ any framework infra may call it
//     (the original documented "open caller" intent, now compiler-enforced
//     against the real threat class = business code). Content stays Hard via
//     the frameworkHTTPIDPrefix A-class panic.
//   - NewEventDerivation upstream: Medium → Hard. The compiler refuses
//     non-runtime imports; content stays Hard via the embedded
//     ContractSpec.Validate(). The former single-file ("only eventrouter")
//     within-runtime allowlist + its drift guard are RETIRED: once the
//     compiler seals non-runtime callers and Validate() seals content, the
//     single-caller rule guarded only a non-security tracing projection whose
//     output must still pass Validate() — pure ceremony. If a future concern
//     needs single-package Hard, move NewEventDerivation into the eventrouter
//     package as an unexported helper (cost: one NO-MANUAL exclusion).
//   - ContractSpec{…} literal ban: downstream Hard (unchanged,
//     NO-MANUAL-CONTRACTSPEC-LITERAL-01). runtime/internal/contractbuild is the
//     sanctioned funnel home and is excluded from that scan, analogous to the
//     former kernel/contractspec/** exclusion.
//
// ref: runtime/internal/authtest (#638 internal/ Medium→Hard precedent);
// docs/reviews/202605181109-042-archtest-six-agent-audit.md §3b/§4 row 9.
package contractbuild
