// outbox_reconstruction_caller.go — importable rule body for
// OUTBOX-RECONSTRUCTION-CALLER-01. Migrated from the legacy _test.go form to a
// non-test .go (M3 #1302 / #1635) so external Cell repos can import and run it
// via StandardCellRules / RunStandardCellRules. The dogfood + anti-vacuity
// reverse self-check live in outbox_reconstruction_caller_test.go.
//
// # OUTBOX-RECONSTRUCTION-CALLER-01
//
// outbox.Entry is sealed construction (OUTBOX-ENTRY-SEALED-CONSTRUCTION-01): a
// populated composite literal outside kernel/outbox is a compile error. That
// makes Entry FORGERY-BY-LITERAL unrepresentable (type-system Hard upstream).
// But Entry has two cross-package RECONSTRUCTION funnels that legitimately must
// exist — and both accept attacker-controllable Principal / OccurredAt without
// any provenance check (Entry.Validate only checks well-formedness, never
// "where did this Principal come from"):
//
//   - outbox.UnmarshalEnvelope(topic, raw []byte) — decodes arbitrary wire bytes
//     into a sealed Entry. A caller that crafts the JSON controls the principal.
//   - outbox.EntryScan{...}.ToEntry() — rebuilds a sealed Entry from exported
//     scan-target fields. A caller that fills EntryScan controls the principal.
//
// If a business producer (cells/* or examples/*) could call either funnel, it
// would have a second path — alongside ctxkeys-forgery (CTXKEYS-PRINCIPAL-WRITE-
// CALLER-01) — to fabricate an Entry with a forged audit identity and Emit it,
// defeating the "NewEntry-from-ctx is the single injection trust boundary"
// claim. This archtest pins the callsite identity of both funnels to the
// sanctioned framework-infrastructure files: storage adapters (which reconstruct
// from already-persisted DB rows) and wire/consumer decoders (which decode
// inbound broker bytes). Neither funnel is reachable from a producer.
//
// Architectural note (why no runtime guard on Emit): the relay and the consumer
// path never feed a reconstructed Entry back into Emitter.Emit — the relay
// MarshalEnvelopes the claimed Entry straight to Publisher.Publish, and the
// consumer dispatches the decoded Entry to a handler. So locking the two
// reconstruction callsites is sufficient; Emit needs no extra check.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD by archtest caller-allowlist. The callee is resolved via
//     go/types (ResolvePackageRef / ResolveMethodCall / types.Info.Uses), so
//     import aliases (kout "…/kernel/outbox"), dot-imports (bare-identifier
//     UnmarshalEnvelope), and method-expression forms are all resolved to the
//     same symbol — there is no "looks like but isn't" gap. The allowlist is
//     bound to PLATFORM PACKAGE IDENTITY (isGoCellPlatformPkgPath), not a bare
//     repo-relative path, so a consumer module that forges an allowlisted rel
//     path (e.g. its own "runtime/eventbus/eventbus.go") is NOT exempt — the
//     rule stays a true pure ban for external repos. Any callsite outside the
//     allowlist fails in CI.
//   - Upstream: MEDIUM, and this is a GO-LANGUAGE CEILING, not a deferred TODO.
//     Hard upstream would require the two funnels to be unreachable outside the
//     sanctioned packages. They cannot be: reconstruction is inherently
//     cross-package (adapters/postgres, runtime/eventbus, adapters/rabbitmq are
//     all distinct packages from kernel/outbox), so Go visibility cannot express
//     "only these packages may call an exported func/method". This is the same
//     permanent ceiling documented for SPAN-SETATTR-HOLDER-SEAL (#851) and
//     HEALTHZ-HOLDER-SEAL (#893 won't-do); tracked here as #1282. The downstream
//     archtest is the enforcement; the ceiling is documented, not silently accepted.
//
// # Detection is REFERENCE-based, not call-based
//
// The scanner walks BOTH SelectorExpr and bare *ast.Ident references that
// go/types resolves to a funnel symbol — whether the reference is the callee of
// a call OR passed as a function/method value. This deliberately closes the
// "indirection through a function value" gap: a file that does
// `f := outbox.UnmarshalEnvelope; f(b)` references the symbol at the
// `outbox.UnmarshalEnvelope` SelectorExpr and is caught. The bare-Ident walk
// additionally closes the dot-import form (import . "…/kernel/outbox";
// UnmarshalEnvelope(b)), which references the package func as a bare *ast.Ident —
// resolved via types.Info.Uses, alias/dot-import-proof. The mirror of
// scaffold_derived_forceoverwrite.go's dual SelectorExpr+Ident walk.
// (The (EntryScan).ToEntry method funnel is always a SelectorExpr — a method call
// on a receiver value — even under a dot-import of kernel/outbox, so only the
// package func has a bare-Ident form.)
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - A bypass added in a //go:build-gated PRODUCTION file under a tag not in the
//     default build context would be missed by the default-tags scan. The funnel
//     callers today are all default-build; the integration-tagged code is _test.go
//     (excluded from production load anyway). Mitigated by the default+tagged
//     double scan when cfg.BuildTags is supplied. Documented.
//   - The anti-vacuity guard (every allowlisted file must reference its funnel ≥1×)
//     lives in the _test.go dogfood (checkReconstructionAntiVacuity) — NOT in the
//     importable Check (which returns only forward caller-allowlist violations).
//     It proves the scanner actually resolves the real references (not a vacuous
//     pass) AND forbids stale allowlist rot — a dead allowlist entry is a latent
//     bypass slot, so an entry that no longer corresponds to a real reference
//     fails the dogfood (charter "no silent carve-over / empty steady state").
//
// # External Cell repo semantics
//
// This rule is registered in StandardCellRules. The allowlisted infra files live
// in GoCell's own adapters/ and runtime/ packages, which are dependencies of an
// external Cell repo — not in the consumer's own module. Therefore in a clean
// external Cell repo there are zero references to either funnel in the scanned
// source, and the rule degrades to a PURE BAN (vacuous-green). That is intended:
// business / consumer code must never call UnmarshalEnvelope or EntryScan.ToEntry.
// importable non-test home; external repos run it via StandardCellRules;
// GoCell dogfoods it via the _test.go.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ruleOutboxReconstructionCaller01 is the stable rule ID for
// OUTBOX-RECONSTRUCTION-CALLER-01.
const ruleOutboxReconstructionCaller01 = "OUTBOX-RECONSTRUCTION-CALLER-01"

// reconstructionOutboxPkg is the import path of kernel/outbox, derived from
// PlatformModulePath so a module rename / /v2 bump updates one place.
const reconstructionOutboxPkg = PlatformModulePath + "/kernel/outbox"

// reconstructionFunnelAllowlist maps each Entry-reconstruction funnel symbol to
// the module-relative production files that may call it. The two funnels have
// DISTINCT sanctioned callers — storage reconstruction vs wire decode — so the
// allowlist is per-symbol, not a shared infra blanket.
//
// External Cell repo note: a relative path alone is not a trustworthy identity in
// a consumer module. The allowlist is consulted by isReconstructionAllowedSite,
// which FIRST requires the calling package to be a GoCell platform package
// (isGoCellPlatformPkgPath) — these rel paths exist only inside adapters/ and
// runtime/ packages that ship as GoCell dependencies. A consumer module that
// forges one of these rel paths has a non-platform package path and is correctly
// NOT exempt, so the rule degrades to a true pure ban there.
var reconstructionFunnelAllowlist = map[string]map[string]struct{}{
	// Wire-decode funnel: inbound broker bytes → sealed Entry.
	"kernel/outbox.UnmarshalEnvelope": {
		"runtime/eventbus/eventbus.go":    {}, // EventBus inbound decode → subscriber dispatch
		"adapters/rabbitmq/subscriber.go": {}, // AMQP delivery decode
		"adapters/mqtt/subscriber.go":     {}, // MQTT PUBLISH delivery decode (processDelivery)
	},
	// Storage-reconstruction funnel: persisted DB row → sealed Entry.
	"(kernel/outbox.EntryScan).ToEntry": {
		"adapters/postgres/outbox_store.go": {}, // PG relay claim/scan path
		// PG projection journal replay (#1368): reconstructs a sealed Entry from
		// outbox_entries rows for the projection ReplaySource. Same sanctioned
		// "persisted DB row → Entry" infra role as the relay claim path; producers
		// (cells/examples) cannot reach it.
		"adapters/postgres/projection_replay_source.go":  {},
		"runtime/outbox/outboxtest/store_conformance.go": {}, // store-conformance helper lib (not _test.go)
	},
}

// CheckOutboxReconstructionCaller01 enforces the FORWARD leg of
// OUTBOX-RECONSTRUCTION-CALLER-01: every production reference to
// outbox.UnmarshalEnvelope and (outbox.EntryScan).ToEntry must occur in a
// sanctioned site — a GoCell platform package (isGoCellPlatformPkgPath) whose
// module-relative path is listed in reconstructionFunnelAllowlist. This is the
// portable, externally-runnable half of the rule.
//
// The REVERSE leg — anti-vacuity / no-stale-allowlist (every allowlisted file
// must still host a live reference) — is a GoCell-internal self-check
// (checkReconstructionAntiVacuity, exercised by the _test.go dogfood), NOT part
// of this Check: it reasons about GoCell's own allowlist hygiene, which is
// meaningless in an external consumer module.
//
// It scans the running module's production code (Production → findModuleRoot),
// covering tag-gated files via cfg.BuildTags, and returns the diagnostics it
// observes without calling t.Errorf (the caller — typically Report or
// RunStandardCellRules — funnels diagnostics uniformly).
//
// External Cell repo semantics: the allowlisted infra files live in GoCell's own
// adapters/runtime packages, not in a consumer's own source. A clean consumer
// module has zero references to either funnel in its own code → vacuous-green,
// no false positives.
func CheckOutboxReconstructionCaller01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	// Scan twice per the ConfigForExternalCell.BuildTags contract: default build
	// config first (so files behind //go:build !<tag> are not missed), then the
	// tagged config when cfg.BuildTags is non-empty (so files behind //go:build
	// <tag> are covered). A tagged-only load EXCLUDES default-only files, so a
	// single tagged pass would leave a hole — same default+tagged shape as
	// CheckPanicRegistered / CheckScaffoldDerivedForceOverwrite.
	// scanner.Canonical dedups the overlap (unconstrained files are loaded by both
	// passes).
	out := Run(t, Production(TypedOpts{}), collectReconstructionCallerViolations)
	if len(cfg.BuildTags) > 0 {
		out = append(out, Run(t, Production(TypedOpts{Tags: cfg.BuildTags}), collectReconstructionCallerViolations)...)
	}
	return scanner.Canonical(out)
}

// isReconstructionAllowedSite reports whether (pkgPath, symbol, rel) identifies a
// sanctioned Entry-reconstruction site: the calling package must be a GoCell
// platform package (so a consumer module forging an allowlisted rel path is NOT
// exempt — closing the registered rule's pure-ban bypass) AND the
// module-relative file must be listed for that symbol in
// reconstructionFunnelAllowlist. Extracted as a pure function so the
// platform-identity bind is unit-testable (TestIsReconstructionAllowedSite).
func isReconstructionAllowedSite(pkgPath, symbol, rel string) bool {
	if !isGoCellPlatformPkgPath(pkgPath) {
		return false
	}
	_, ok := reconstructionFunnelAllowlist[symbol][rel]
	return ok
}

// reconstructionViolationMessage is the diagnostic body shared by the SelectorExpr
// and bare-Ident (dot-import) reference walkers.
func reconstructionViolationMessage(symbol, rel string) string {
	return fmt.Sprintf(
		"OUTBOX-RECONSTRUCTION-CALLER-01: %s is called from %s, which is not a sanctioned "+
			"Entry-reconstruction site. This funnel rebuilds a sealed outbox.Entry from "+
			"untrusted input (wire bytes / scan fields) and Entry.Validate does NOT check "+
			"principal provenance — a producer calling it could forge an audit identity. "+
			"Only storage adapters and wire/consumer decoders may reconstruct; route producer "+
			"event creation through outbox.NewEntry. If this IS a new sanctioned infra site, "+
			"add it to reconstructionFunnelAllowlist with rationale.",
		symbol, rel,
	)
}

// collectReconstructionCallerViolations is the per-Pass forward scanner shared by
// the production Check and the anti-vacuity reverse self-check. It returns forward
// caller-allowlist violations (references outside the sanctioned platform sites).
// It walks BOTH SelectorExpr references (qualified / aliased / method / function
// value) AND bare *ast.Ident references (dot-import form of the UnmarshalEnvelope
// package func), mirroring scaffold_derived_forceoverwrite.go. The anti-vacuity
// check (stale allowlist entries) is handled separately by
// checkReconstructionAntiVacuity, which requires the observed map accumulated
// across the full production scan.
func collectReconstructionCallerViolations(p *Pass) []Diagnostic {
	if !p.Typed() {
		return nil
	}
	pkgPath := p.Pkg.Path()
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		// Qualified / aliased / method / function-value forms (SelectorExpr).
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			symbol, matched := reconstructionFunnelSymbol(p.TypesInfo, sel)
			if !matched || isReconstructionAllowedSite(pkgPath, symbol, rel) {
				return
			}
			d = append(d, Diagnostic{
				Rel:     rel,
				Line:    p.Fset.Position(sel.Pos()).Line,
				Message: reconstructionViolationMessage(symbol, rel),
			})
		})
		// Dot-import bare-identifier form of the UnmarshalEnvelope package func.
		// Idents that are the .Sel of a SelectorExpr are already handled above.
		EachInSubtree[ast.Ident](file, func(ident *ast.Ident) {
			if isInsideSelectorExpr(file, ident) {
				return
			}
			symbol, matched := reconstructionFunnelSymbolFromIdent(p.TypesInfo, ident)
			if !matched || isReconstructionAllowedSite(pkgPath, symbol, rel) {
				return
			}
			d = append(d, Diagnostic{
				Rel:     rel,
				Line:    p.Fset.Position(ident.Pos()).Line,
				Message: reconstructionViolationMessage(symbol, rel),
			})
		})
	}
	return d
}

// observeReconstructionCallerRefs accumulates the set of (symbol, relFile) pairs
// observed across a production scan. It is used by the anti-vacuity check in the
// _test.go dogfood to prove every allowlisted file still hosts a live reference.
func observeReconstructionCallerRefs(p *Pass) map[string]map[string]struct{} {
	if !p.Typed() {
		return nil
	}
	observed := map[string]map[string]struct{}{}
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			symbol, matched := reconstructionFunnelSymbol(p.TypesInfo, sel)
			if !matched {
				return
			}
			if observed[symbol] == nil {
				observed[symbol] = map[string]struct{}{}
			}
			observed[symbol][rel] = struct{}{}
		})
	}
	return observed
}

// checkReconstructionAntiVacuity returns stale-allowlist diagnostics: for each
// (symbol, file) in reconstructionFunnelAllowlist, if no live reference was
// observed in observed[symbol][file], a diagnostic is emitted. A stale entry is
// a latent bypass slot.
func checkReconstructionAntiVacuity(observed map[string]map[string]struct{}) []Diagnostic {
	var d []Diagnostic
	for symbol, files := range reconstructionFunnelAllowlist {
		allowed := make([]string, 0, len(files))
		for f := range files {
			allowed = append(allowed, f)
		}
		sort.Strings(allowed)
		for _, f := range allowed {
			if _, seen := observed[symbol][f]; !seen {
				d = append(d, Diagnostic{
					Message: fmt.Sprintf(
						"OUTBOX-RECONSTRUCTION-CALLER-01: allowlist entry %q for %s is STALE — no live "+
							"call observed. Either the scanner stopped detecting the call (regression) or the "+
							"call was removed; drop the dead allowlist entry so it cannot become a silent bypass slot.",
						f, symbol,
					),
				})
			}
		}
	}
	return d
}

// reconstructionFunnelSymbol resolves a SelectorExpr REFERENCE (call or value)
// to one of the two Entry-reconstruction funnel symbols, alias-proof via
// go/types. Returns ("", false) for any other reference. The returned key
// matches the reconstructionFunnelAllowlist keys.
func reconstructionFunnelSymbol(info *types.Info, sel *ast.SelectorExpr) (string, bool) {
	// Package-func funnel: outbox.UnmarshalEnvelope (qualified reference).
	if pkgPath, name, ok := ResolvePackageRef(info, sel); ok {
		if pkgPath == reconstructionOutboxPkg && name == "UnmarshalEnvelope" {
			return "kernel/outbox.UnmarshalEnvelope", true
		}
		return "", false
	}
	// Method funnel: (outbox.EntryScan).ToEntry (handles pointer/value/method-value).
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil || fn.Name() != "ToEntry" || fn.Pkg() == nil || fn.Pkg().Path() != reconstructionOutboxPkg {
		return "", false
	}
	if !isEntryScanReceiver(fn) {
		return "", false
	}
	return "(kernel/outbox.EntryScan).ToEntry", true
}

// reconstructionFunnelSymbolFromIdent resolves a bare *ast.Ident (the dot-import
// form of the kernel/outbox.UnmarshalEnvelope package func) to its funnel symbol
// via types.Info.Uses, alias/dot-import-proof. Returns ("", false) for any other
// ident. The (EntryScan).ToEntry method funnel has no bare-Ident form — a method
// call is always a SelectorExpr on the receiver value — so only the package func
// is resolved here.
func reconstructionFunnelSymbolFromIdent(info *types.Info, ident *ast.Ident) (string, bool) {
	if info == nil || ident == nil || ident.Name != "UnmarshalEnvelope" {
		return "", false
	}
	obj, ok := info.Uses[ident]
	if !ok {
		return "", false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != reconstructionOutboxPkg {
		return "", false
	}
	return "kernel/outbox.UnmarshalEnvelope", true
}

// isEntryScanReceiver reports whether fn's receiver base type is
// kernel/outbox.EntryScan (value or pointer), so an unrelated future ToEntry on
// another type in the package does not match.
func isEntryScanReceiver(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	t := sig.Recv().Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}
	return named.Obj().Name() == "EntryScan"
}
