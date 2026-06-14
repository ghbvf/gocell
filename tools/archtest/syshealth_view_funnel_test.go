//go:build archtest

// INVARIANT: SYSHEALTH-VIEW-CTX-FUNNEL-01
//
// syshealth_view_funnel_test.go — production-callsite allowlist for the
// runtime/syshealth HealthView context funnel.
//
// The funnel's WRITE path is already Hard: healthViewKey is an unexported type
// (runtime/syshealth/context.go), so no package outside syshealth can inject a
// HealthView through any path other than syshealth.WithHealthView — a compile
// error otherwise. This rule adds the half the godoc claims ("sole injector /
// sole reader") but the type system cannot express: CALLSITE BREADTH — which
// production files may call the exported funnel funcs at all.
//
//   - WithHealthView (the SOLE injector) may be called only from the bootstrap
//     wiring point that builds the primary-listener request-context chain.
//   - HealthViewFromContext (the SOLE reader) may be called only from the syscore
//     healthread service that serves http.admin.health.cells.v1.
//   - _test.go files are exempt: the funnel is a production-callsite guard, and
//     the view/context/injector tests legitimately round-trip it.
//
// # AI-robust rating (charter §分级 — funnel: state upstream + downstream)
//
//   - WRITE seal = Hard (unexported key; out-of-package injection is a compile
//     error — owned by context.go, not this scan).
//   - CALLSITE breadth = Medium (type-aware caller scan). "Only these files may
//     call the exported funnel funcs" is not expressible in Go's type system —
//     the funcs must stay exported so bootstrap (writer) and healthread (reader)
//     can call them across packages — so the Go ceiling for the breadth
//     obligation is this governance-style scan. The READER side has NO Hard
//     backstop; this scan is its only guard. The Hard-ization path (route-group-
//     scoped injection, or a sealed/private injector token making a production
//     view unconstructible outside bootstrap) is tracked in a backlog issue.
//
// # Anti-vacuity (charter §archtest 文件命名)
//
//	The production dogfood asserts ≥1 allowlisted caller is observed for EACH
//	func; if WithHealthView / HealthViewFromContext were renamed or removed the
//	scan would silently find nothing and this fails loud.
//
// # Synthetic red (charter §archtest 文件命名)
//
//	TestSyshealthViewCtxFunnel01_RedSelfCheck re-runs the SAME scan over
//	production with the allowlists EMPTIED and asserts the real callers are then
//	flagged — proving the gate bites rather than passing as a no-op.
package archtest

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ruleSyshealthViewCtxFunnel = "SYSHEALTH-VIEW-CTX-FUNNEL-01"
	// syshealthPkgPath is the canonical import path of runtime/syshealth, anchored
	// to PlatformModulePath so a module rename updates exactly one place.
	syshealthPkgPath        = PlatformFrameworkModulePath + "/runtime/syshealth"
	withHealthViewFn        = "WithHealthView"
	healthViewFromContextFn = "HealthViewFromContext"
)

// syshealthViewWriterAllowlist is the set of production files permitted to call
// syshealth.WithHealthView (the SOLE injector) — only the bootstrap primary-
// listener wiring injects the view.
var syshealthViewWriterAllowlist = map[string]bool{
	"runtime/bootstrap/phases_http.go": true,
}

// syshealthViewReaderAllowlist is the set of production files permitted to call
// syshealth.HealthViewFromContext (the SOLE reader) — only the syscore healthread
// service consumes the view to serve http.admin.health.cells.v1.
var syshealthViewReaderAllowlist = map[string]bool{
	"corecells/syscore/slices/healthread/service.go": true,
}

// syshealthFunnelScan accumulates a production scan's verdict: violations plus
// per-func observed (in-allowlist) and flagged (out-of-allowlist) counts.
type syshealthFunnelScan struct {
	diags          []Diagnostic
	observedWriter int
	observedReader int
	flaggedWriter  int
	flaggedReader  int
}

// scanSyshealthViewFunnel scans one file for calls to the two funnel funcs,
// classifying each as observed (caller in allowlist) or flagged (out). _test.go
// files are exempt — the funnel guards production callsites only. The allowlists
// are parameters so the red self-check can drive the same scan with empty sets.
func scanSyshealthViewFunnel(p *Pass, file *ast.File, writerAllow, readerAllow map[string]bool) syshealthFunnelScan {
	var sc syshealthFunnelScan
	rel := p.Rel(file)
	if strings.HasSuffix(rel, "_test.go") {
		return sc
	}
	relSlash := filepath.ToSlash(rel)
	info := p.TypesInfo
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := resolvePkgFuncCall(info, call)
		if !ok || pkgPath != syshealthPkgPath {
			return
		}
		switch name {
		case withHealthViewFn:
			if writerAllow[relSlash] {
				sc.observedWriter++
			} else {
				sc.flaggedWriter++
				sc.diags = append(sc.diags, syshealthFunnelDiag(p, call, rel, name, "injector"))
			}
		case healthViewFromContextFn:
			if readerAllow[relSlash] {
				sc.observedReader++
			} else {
				sc.flaggedReader++
				sc.diags = append(sc.diags, syshealthFunnelDiag(p, call, rel, name, "reader"))
			}
		}
	})
	return sc
}

func syshealthFunnelDiag(p *Pass, call *ast.CallExpr, rel, fn, role string) Diagnostic {
	return Diagnostic{
		Rel:  rel,
		Line: p.Fset.Position(call.Pos()).Line,
		Message: ruleSyshealthViewCtxFunnel + ": " + rel + " calls syshealth." + fn +
			" — the HealthView funnel's " + role + " is restricted to its sanctioned production callsite " +
			"(writer: runtime/bootstrap; reader: corecells/syscore healthread). Route through it, or extend " +
			"the allowlist only with a matching carve-out ADR amendment.",
	}
}

// runSyshealthFunnelScan runs the funnel scan over the whole production set with
// the given allowlists and returns the merged verdict (counts summed across
// every production package Pass).
func runSyshealthFunnelScan(t *testing.T, writerAllow, readerAllow map[string]bool) syshealthFunnelScan {
	t.Helper()
	var total syshealthFunnelScan
	_ = Run(t, Production(TypedOpts{Tags: FlatNonDefaultTags()}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			sc := scanSyshealthViewFunnel(p, file, writerAllow, readerAllow)
			total.diags = append(total.diags, sc.diags...)
			total.observedWriter += sc.observedWriter
			total.observedReader += sc.observedReader
			total.flaggedWriter += sc.flaggedWriter
			total.flaggedReader += sc.flaggedReader
		}
		return nil
	})
	return total
}

// TestSyshealthViewCtxFunnel01 is the production dogfood: every production call
// to syshealth.WithHealthView / HealthViewFromContext must come from an
// allowlisted file, and at least one allowlisted caller of each must exist
// (anti-vacuity).
func TestSyshealthViewCtxFunnel01(t *testing.T) {
	t.Parallel()
	sc := runSyshealthFunnelScan(t, syshealthViewWriterAllowlist, syshealthViewReaderAllowlist)
	if sc.observedWriter == 0 {
		t.Fatalf("%s: anti-vacuity — no allowlisted syshealth.WithHealthView caller observed; "+
			"the writer funnel scan is vacuous (renamed/removed?)", ruleSyshealthViewCtxFunnel)
	}
	if sc.observedReader == 0 {
		t.Fatalf("%s: anti-vacuity — no allowlisted syshealth.HealthViewFromContext caller observed; "+
			"the reader funnel scan is vacuous (renamed/removed?)", ruleSyshealthViewCtxFunnel)
	}
	Report(t, ruleSyshealthViewCtxFunnel, sc.diags)
}

// TestSyshealthViewCtxFunnel01_RedSelfCheck is the synthetic red case: with the
// allowlists emptied, the real production callers MUST be flagged — proving the
// gate bites rather than passing as a no-op.
func TestSyshealthViewCtxFunnel01_RedSelfCheck(t *testing.T) {
	t.Parallel()
	empty := map[string]bool{}
	sc := runSyshealthFunnelScan(t, empty, empty)
	if sc.flaggedWriter == 0 {
		t.Errorf("%s self-check: emptying the writer allowlist flagged no syshealth.WithHealthView caller — "+
			"the gate is a no-op (it would not catch an unsanctioned injector)", ruleSyshealthViewCtxFunnel)
	}
	if sc.flaggedReader == 0 {
		t.Errorf("%s self-check: emptying the reader allowlist flagged no syshealth.HealthViewFromContext caller — "+
			"the gate is a no-op (it would not catch an unsanctioned reader)", ruleSyshealthViewCtxFunnel)
	}
}
