// prod_main_wiring_noop_reject.go — importable rule body for
// PROD-MAIN-WIRING-NOOP-REJECT-01. Lives in a non-test .go (like
// outbox_reconstruction_caller.go) so an external Cell repo can import and run it
// via StandardCellRules / RunStandardCellRules. The dogfood, detector reds, green
// fixture, anti-vacuity and matcher unit tests live in prod_main_wiring_test.go.
//
// # PROD-MAIN-WIRING-NOOP-REJECT-01
//
// A production composition-root (main) package — those a consumer declares in
// cfg.ProductionMainPkgs — must not DIRECTLY construct a raw "noop / degraded"
// outbox event sink. Raw noop sinks silently drop events (or lose L2 atomicity);
// in a production binary that is data loss, not a missing feature. The five
// forbidden symbols are the complete raw-noop sink surface of kernel/outbox:
//
//	kernel/outbox.NoopWriter        — Writer that validates then discards every entry
//	kernel/outbox.NewNoopEmitter    — Emitter backed by NoopWriter (discards)
//	kernel/outbox.NewDirectEmitter  — DirectEmitter ctor that bypasses ResolveEmitter
//	kernel/outbox.DiscardPublisher  — Publisher that logs+discards (the "noop publisher")
//	kernel/outbox.DemoTxRunner      — pass-through TxRunner (no real transaction)
//
// All five implement (or back) cell.Nooper and are rejected at runtime by
// outbox.CheckNotNoop in DurabilityDurable mode. This rule is the STATIC,
// pre-Init counterpart: a composition root could mint one before any cell Init
// runs, where no pool/cell probe can see it.
//
// Sanctioned — deliberately NOT forbidden (the rule pushes raw noop THROUGH
// these; banning them would break legitimate wiring). Split by altitude so a
// composition-root author and a cell author each see their own sanctioned set:
//
// Composition-root (what a main package SHOULD call instead of a raw sink):
//
//	outbox.DemoCellEmitter()    — sealed demo emitter funnel (sanctioned way to
//	                              inject a noop emitter into a cell Option)
//	outbox.DemoCellTxManager()  — sealed demo tx-manager funnel
//	outbox.ResolveEmitter       — durable-vs-direct emitter resolver
//	runtime/eventbus.New / InMemoryEventBus — the sole sanctioned in-process
//	                              publisher (no real-broker alternative is wired
//	                              anywhere); excluding it is option A of issue #1303.
//	                              The "in-memory publisher in postgres mode" gap is
//	                              real but out of scope — recorded in ADR
//	                              202605281200 (#1303 row) and tracked as backlog
//	                              issue #1940, not silently accepted.
//
// Cell-level (used inside a cell's Init, not a composition root — listed so the
// rule's relationship to them is unambiguous, NOT as main-package guidance):
//
//	outbox.ResolveCellEmitter   — cell-level durable-vs-direct resolver
//	outbox.NewDirectCellEmitter — intended L4 direct-publish-by-design production
//
// Rule semantic: raw noop sinks must route through the sealed demo funnels or
// wire real infra.
//
// # Non-redundant failure domain (contract-fanout)
//
// Three existing guards touch noop outbox; none scans main-package wiring:
//   - outbox.CheckNotNoop — RUNTIME, inside a cell's Init, durable mode only.
//   - OUTGUARD-01 — METADATA, over cell.yaml durabilityMode.
//   - CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 — archtest over a CELL package's Init.
//
// This rule's domain is the COMPOSITION-ROOT (main) package, statically, before
// any cell Init.
//
// # AI-robust rating (charter §"Funnel 双向锁评级"), same shape as
// OUTBOX-RECONSTRUCTION-CALLER-01
//
//   - Downstream HARD. The reference is resolved via go/types (ResolvePackageRef
//     over both SelectorExpr and dot-import bare *ast.Ident), so import alias,
//     dot-import, and function-value forms all resolve to the same symbol — no
//     "looks-like-but-isn't" gap. There is NO allowlist (unlike reconstruction):
//     within the scanned main pkgs this is a PURE BAN — zero exemption surface,
//     zero allowlist-rot. The anti-vacuity self-test
//     (TestProdMainWiringNoopReject_AntiVacuity_CorebundleInScope) proves the
//     workspace Production scan reaches the separate cmd/corebundle module, so a
//     0-files-scanned vacuous-green cannot pass as a clean dogfood (a downstream
//     Medium→Soft regression guard).
//   - Upstream MEDIUM — permanent Go ceiling, NOT a deferred TODO. Go visibility
//     cannot forbid a main package from calling a PUBLIC func/type; the five
//     symbols MUST stay public (tests / demos / examples legitimately construct
//     them). Sealing them is unreachable without breaking those callers. The
//     consumer's ProductionMainPkgs declaration completeness is also consumer-side
//     (same ceiling as BuildTags). Same permanent-ceiling family as #851/#893/#1282.
//   - Charter single-grade floor = Medium (weakest leg governs).
//
// # 强制盲区自检 (reverse self-test backs each claim)
//
//   - import alias — CAUGHT (go/types). Backed by red_aliasimport fixture.
//   - dot-import bare ident — CAUGHT (ast.Ident walk). Backed by red_dotimport.
//   - function value (`f := outbox.NewNoopEmitter`) — CAUGHT (reference walk, not
//     call-only). Backed by the NewDirectEmitter function-value line in red_qualified.
//   - cross-package call-graph transitivity / factory wrapping — NOT CAUGHT: a main
//     pkg that calls a helper in ANOTHER (non-scanned) package which constructs the
//     sink is invisible to this callsite scan. Permanent Go-language ceiling (a
//     callsite scan cannot follow arbitrary cross-package call graphs; same family
//     as reconstruction's upstream Medium). Mitigation: the established wiring goes
//     through composition.NewSharedDeps / the sealed funnels, and a durable
//     assembly still trips runtime CheckNotNoop. Documented, not silently accepted.
//   - //go:build-tagged production files under a non-default tag — covered by the
//     default + cfg.BuildTags double scan (scanner.Canonical dedups the overlap).
//
// # External Cell repo semantics
//
// Registered in StandardCellRules. Opt-in by declaration: an external repo lists
// its composition roots in cfg.ProductionMainPkgs; an empty slice means "do not
// scan composition roots" (mirrors BuildTags) — no false positives on a repo that
// has not opted in. The forbidden symbols live in GoCell's kernel/outbox, imported
// as a dependency, so the go/types resolution works identically in a consumer module.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ruleProdMainWiringNoopReject01 is the stable rule ID.
const ruleProdMainWiringNoopReject01 = "PROD-MAIN-WIRING-NOOP-REJECT-01"

// prodMainOutboxPkg is the import path of kernel/outbox, derived from
// PlatformModulePath so a module rename / /v2 bump updates one place.
const prodMainOutboxPkg = PlatformModulePath + "/kernel/outbox"

// prodMainOutboxRel is kernel/outbox's module-relative path ("kernel/outbox"),
// derived from prodMainOutboxPkg so the diagnostic display symbol tracks a module
// rename instead of a hardcoded literal prefix.
var prodMainOutboxRel = strings.TrimPrefix(prodMainOutboxPkg, PlatformModulePath+"/")

// prodMainForbiddenSinks maps each forbidden kernel/outbox symbol (exported name)
// to the sanctioned replacement named in its diagnostic. This is the complete
// raw-noop sink surface of kernel/outbox (#1303 completeness audit). Sealed demo
// funnels (DemoCellEmitter / DemoCellTxManager), the intended-L4 NewDirectCellEmitter,
// and the durable resolver (ResolveEmitter) are deliberately ABSENT — the rule
// pushes raw noop through those, it does not ban them.
var prodMainForbiddenSinks = map[string]string{
	"NoopWriter":       "outbox.DemoCellEmitter() (sealed demo funnel) or a real outbox.Writer",
	"NewNoopEmitter":   "outbox.DemoCellEmitter() (sealed demo funnel) or outbox.ResolveEmitter",
	"NewDirectEmitter": "outbox.ResolveEmitter / outbox.NewDirectCellEmitter (sealed)",
	"DiscardPublisher": "a real outbox.Publisher (DiscardPublisher is a test/demo-only discard sink)",
	"DemoTxRunner":     "outbox.DemoCellTxManager() (sealed demo funnel) or a real persistence.TxRunner",
}

// CheckProdMainWiringNoopReject enforces PROD-MAIN-WIRING-NOOP-REJECT-01: no
// production composition-root package listed in cfg.ProductionMainPkgs may
// directly construct a forbidden raw-noop sink. It returns the diagnostics it
// observes without calling t.Errorf (the caller — Report / RunStandardCellRules —
// funnels diagnostics uniformly).
//
// It scans the workspace Production package set (go.work-aware, so the separate
// cmd/corebundle module is reachable) and keeps only Passes whose package dir
// matches a declared main-pkg pattern. cfg.BuildTags drives a second tagged scan
// so a sink behind //go:build <tag> is not missed; scanner.Canonical dedups the
// overlap.
//
// Fail-closed on a no-op gate: every declared pattern that matched NO production
// package across BOTH scans yields a diagnostic (prodMainUnmatchedPatternDiags). A
// 0-match pattern — a typo, a path a refactor moved, a pattern written relative to
// the wrong root, or a deliberately-unsupported whole-module ("." / "./...") or
// absolute form (see matchesMainPkg) — would otherwise be a SILENT false green: the
// consumer believes the guard is active while it scanned nothing. This closes the
// vacuous-green hole (#1942 review cluster C1).
func CheckProdMainWiringNoopReject(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if len(cfg.ProductionMainPkgs) == 0 {
		// Opt-in by declaration (mirrors BuildTags): a consumer that does not list
		// its composition roots gets no main-pkg scan. GoCell's own dogfood passes
		// ./cmd/corebundle and the anti-vacuity self-test proves it is in scope, so
		// this early return is not a silent vacuous green for GoCell. Emit a visible
		// advisory (NOT an error) so a consumer's CI log distinguishes "deliberately
		// opted out" from "wired RunStandardCellRules but forgot to declare composition
		// roots" — the latter would otherwise be an invisible no-coverage green.
		t.Logf("%s: ProductionMainPkgs is empty — composition-root noop scan SKIPPED. "+
			"Declare your main packages (e.g. []string{\"./cmd/yourbinary\"}) to enable this rule.",
			ruleProdMainWiringNoopReject01)
		return nil
	}
	mainPkgs := cfg.ProductionMainPkgs
	// matched accumulates, across BOTH the default and the build-tagged scan, which
	// declared patterns hit at least one production package. Any pattern still unset
	// after both scans matched NOTHING — a false-green misconfiguration the rule
	// rejects loud below, never passes silently.
	matched := make(map[string]bool, len(mainPkgs))
	scan := func(p *Pass) []Diagnostic {
		return collectProdMainWiringViolationsInPkgs(p, mainPkgs, matched)
	}
	out := Run(t, Production(TypedOpts{}), scan)
	if len(cfg.BuildTags) > 0 {
		out = append(out, Run(t, Production(TypedOpts{Tags: cfg.BuildTags}), scan)...)
	}
	out = append(out, prodMainUnmatchedPatternDiags(mainPkgs, matched)...)
	return scanner.Canonical(out)
}

// collectProdMainWiringViolationsInPkgs returns the forward reference-walk
// violations for p, but only when p's package belongs to one of mainPkgs. A typed
// Pass is exactly one package, so every file shares one directory — gate the whole
// Pass on the first file's dir. It records every pattern p's dir matches into
// matched (the 0-match accumulator CheckProdMainWiringNoopReject reads after the
// scan to flag patterns that matched nothing).
func collectProdMainWiringViolationsInPkgs(p *Pass, mainPkgs []string, matched map[string]bool) []Diagnostic {
	if !p.Typed() || len(p.Files) == 0 {
		return nil
	}
	if !markMatchingPatterns(path.Dir(p.Rel(p.Files[0])), mainPkgs, matched) {
		return nil
	}
	return collectProdMainWiringViolations(p)
}

// collectProdMainWiringViolations is the per-Pass forward scanner shared by the
// production Check (after main-pkg filtering) and the detector fixture self-tests
// (which call it directly, with no main-pkg filter, because the fixtures do not
// live under a main-pkg dir). It walks BOTH SelectorExpr references (qualified /
// aliased / method / function-value) AND bare *ast.Ident references (dot-import
// form), mirroring outbox_reconstruction_caller.go.
func collectProdMainWiringViolations(p *Pass) []Diagnostic {
	if !p.Typed() {
		return nil
	}
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		// Qualified / aliased / method / function-value / embedded-field forms
		// (SelectorExpr). EachInSubtree walks the whole file AST including type
		// declarations, so an embedded field `struct{ outbox.NoopWriter }` is caught
		// the same as a composite literal — both reference the type via SelectorExpr.
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			if name, ok := prodMainForbiddenSymbol(p.TypesInfo, sel); ok {
				d = append(d, prodMainDiag(p, rel, sel.Pos(), name))
			}
		})
		// Dot-import bare-identifier form. Idents that are the .Sel of a
		// SelectorExpr are already handled above.
		EachInSubtree[ast.Ident](file, func(ident *ast.Ident) {
			if isInsideSelectorExpr(file, ident) {
				return
			}
			if name, ok := prodMainForbiddenSymbol(p.TypesInfo, ident); ok {
				d = append(d, prodMainDiag(p, rel, ident.Pos(), name))
			}
		})
	}
	return d
}

// prodMainForbiddenSymbol resolves a SelectorExpr or bare Ident reference to a
// forbidden kernel/outbox sink symbol, alias/dot-import-proof via go/types.
// Returns the BARE symbol name (a prodMainForbiddenSinks key, e.g. "NoopWriter")
// and ok; ("", false) for any other reference. Returning the bare name keeps the
// diagnostic-display prefix single-sourced from prodMainOutboxRel rather than a
// hardcoded literal.
func prodMainForbiddenSymbol(info *types.Info, expr ast.Expr) (string, bool) {
	pkgPath, name, ok := ResolvePackageRef(info, expr)
	if !ok || pkgPath != prodMainOutboxPkg {
		return "", false
	}
	if _, forbidden := prodMainForbiddenSinks[name]; !forbidden {
		return "", false
	}
	return name, true
}

// prodMainDiag builds the diagnostic for a forbidden reference at pos. name is the
// bare symbol name (a prodMainForbiddenSinks key).
func prodMainDiag(p *Pass, rel string, pos token.Pos, name string) Diagnostic {
	return Diagnostic{
		Rel:     rel,
		Line:    p.Fset.Position(pos).Line,
		Message: prodMainViolationMessage(name),
	}
}

// prodMainViolationMessage is the diagnostic body shared by the SelectorExpr and
// bare-Ident reference walkers. name is the bare symbol name; the display prefix
// (kernel/outbox) is derived from prodMainOutboxRel so it tracks a module rename.
// "constructed or referenced" covers all caught forms — composite literal, call,
// function value, and embedded field (a function value / embed is referenced, not
// constructed).
func prodMainViolationMessage(name string) string {
	return fmt.Sprintf(
		"PROD-MAIN-WIRING-NOOP-REJECT-01: %s.%s is constructed or referenced in a production "+
			"composition-root (main) package; production wiring must not directly mint a raw "+
			"noop/degraded event sink (a pool/cell probe cannot see a main-package noop minted "+
			"before cell Init). Route demo wiring through the sealed funnel or wire real infra: %s.",
		prodMainOutboxRel, name, prodMainForbiddenSinks[name],
	)
}

// prodMainUnmatchedPatternDiags returns one diagnostic per declared ProductionMainPkgs
// pattern that matched NO production package across the scan. matched is the
// accumulator markMatchingPatterns populated. A 0-match pattern is almost always a
// misconfiguration — a typo, a path a refactor moved, a pattern written relative to
// the wrong root, or a deliberately-unsupported whole-module ("." / "./...") or
// absolute form (see matchesMainPkg) — and silently passing it is a false green: the
// composition-root noop guard the consumer believes is active scanned nothing. Each
// diagnostic is anchored to the offending pattern (Rel=pat, the non-file Rel
// convention DISTLOCK-LOCK-NOT-CONTEXT-01 uses for config-level diagnostics);
// diagFile pins Line=1 so the report stays well-formed rather than degrading to ":0:".
func prodMainUnmatchedPatternDiags(patterns []string, matched map[string]bool) []Diagnostic {
	var d []Diagnostic
	for _, pat := range patterns {
		if matched[pat] {
			continue
		}
		d = append(d, diagFile(pat, fmt.Sprintf(
			"%s: ProductionMainPkgs pattern %q matched no production package — the composition-root "+
				"noop guard scanned NOTHING for it (false green). Point it at a real package dir "+
				"RELATIVE TO THE SCAN ROOT (the go.work workspace root, or the module root for a "+
				"single-module repo), e.g. \"./cmd/yourbinary\". Whole-module patterns (\".\", "+
				"\"./...\") and absolute paths are unsupported — a composition root is a SPECIFIC "+
				"package; name it (\"./cmd/yourbinary\") or bound it (\"./cmd/...\").",
			ruleProdMainWiringNoopReject01, pat)))
	}
	return d
}

// matchesMainPkg reports whether relDir (a SCAN-ROOT-relative package directory:
// the go.work workspace root under a workspace, the module root for a single-module
// repo — see findModuleRoot / Pass.Rel) belongs to one of patterns. Patterns are
// Go-style relative package patterns matched root-path-agnostically against the dir:
// "./cmd/x" or "cmd/x" (exact package) and "./cmd/x/..." (recursive prefix). A
// leading "./" is optional. Extracted as a pure function so the matcher is
// unit-testable.
//
// UNSUPPORTED forms — this pure matcher returns false for each (asserted in
// TestMatchesMainPkg), and CheckProdMainWiringNoopReject then escalates any pattern
// that matched nothing to a LOUD 0-match diagnostic (prodMainUnmatchedPatternDiags),
// so they are no longer a silent no-op at the Check level:
//   - whole-module "./..." / "." — a composition root is a SPECIFIC package, never
//     the whole module; banning raw noop everywhere would false-positive on the
//     test/demo helpers that legitimately construct it (the very reason the sinks
//     stay public). Use an explicit "./cmd/x" or a bounded "./cmd/..." instead.
//   - absolute paths — relDir is always scan-root-relative, so an absolute pattern
//     never equals it. Pass scan-root-relative patterns.
func matchesMainPkg(relDir string, patterns []string) bool {
	relDir = path.Clean(relDir)
	for _, pat := range patterns {
		if matchesMainPkgPattern(relDir, pat) {
			return true
		}
	}
	return false
}

// matchesMainPkgPattern reports whether the already-path.Clean'd relDir belongs to
// the single pattern pat. Split out of matchesMainPkg so markMatchingPatterns can
// ask the question per-pattern (to record per-pattern hits) without re-implementing
// the "./"-prefix / "/..." recursive semantics — one matcher, one source of truth.
func matchesMainPkgPattern(relDir, pat string) bool {
	pat = strings.TrimPrefix(pat, "./")
	if rec, ok := strings.CutSuffix(pat, "/..."); ok {
		rec = path.Clean(rec)
		return relDir == rec || strings.HasPrefix(relDir, rec+"/")
	}
	return relDir == path.Clean(pat)
}

// markMatchingPatterns records in matched every pattern that relDir belongs to (a
// dir can satisfy more than one, e.g. "./cmd/x" and "./cmd/..."), and reports
// whether relDir matched at least one. matched is the accumulator
// CheckProdMainWiringNoopReject reads AFTER the scan to flag patterns that matched
// no production package at all (a false-green misconfiguration → loud diagnostic).
func markMatchingPatterns(relDir string, patterns []string, matched map[string]bool) bool {
	relDir = path.Clean(relDir)
	any := false
	for _, pat := range patterns {
		if matchesMainPkgPattern(relDir, pat) {
			matched[pat] = true
			any = true
		}
	}
	return any
}
