//go:build archtest

// INVARIANT: SYSINFO-VIEW-CTX-FUNNEL-01
//
// sysinfo_view_funnel_test.go — production-callsite allowlist for the
// runtime/sysinfo SystemView context funnel.
//
// The funnel's WRITE path is already Hard: systemViewKey is an unexported type
// (runtime/sysinfo/context.go), so no package outside sysinfo can inject a
// SystemView through any path other than sysinfo.WithSystemView. This rule adds
// the callsite breadth guard the type system cannot express:
//
//   - WithSystemView may be called only from the bootstrap primary-listener
//     wiring point.
//   - SystemViewFromContext may be called only from syscore systemread.
//   - _test.go files are exempt: the funnel guards production callsites only.
//
// # AI-robust rating
//
//   - WRITE seal = Hard (unexported key; out-of-package injection is a compile
//     error).
//   - CALLSITE breadth = Medium (type-aware caller scan).
//
// # Anti-vacuity
//
// The production dogfood asserts at least one allowlisted caller for each func.
//
// # Synthetic red
//
// TestSysinfoViewCtxFunnel01_RedSelfCheck empties both allowlists and asserts
// the real production callers are flagged.
package archtest

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ruleSysinfoViewCtxFunnel = "SYSINFO-VIEW-CTX-FUNNEL-01"
	sysinfoPkgPath           = PlatformFrameworkModulePath + "/runtime/sysinfo"
	withSystemViewFn         = "WithSystemView"
	systemViewFromContextFn  = "SystemViewFromContext"
)

var sysinfoViewWriterAllowlist = map[string]bool{
	"runtime/bootstrap/phases_http.go": true,
}

var sysinfoViewReaderAllowlist = map[string]bool{
	"corecells/syscore/slices/systemread/service.go": true,
}

type sysinfoFunnelScan struct {
	diags          []Diagnostic
	observedWriter int
	observedReader int
	flaggedWriter  int
	flaggedReader  int
}

func scanSysinfoViewFunnel(p *Pass, file *ast.File, writerAllow, readerAllow map[string]bool) sysinfoFunnelScan {
	var sc sysinfoFunnelScan
	rel := p.Rel(file)
	if strings.HasSuffix(rel, "_test.go") {
		return sc
	}
	relSlash := filepath.ToSlash(rel)
	info := p.TypesInfo
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := resolvePkgFuncCall(info, call)
		if !ok || pkgPath != sysinfoPkgPath {
			return
		}
		switch name {
		case withSystemViewFn:
			if writerAllow[relSlash] {
				sc.observedWriter++
			} else {
				sc.flaggedWriter++
				sc.diags = append(sc.diags, sysinfoFunnelDiag(p, call, rel, name, "injector"))
			}
		case systemViewFromContextFn:
			if readerAllow[relSlash] {
				sc.observedReader++
			} else {
				sc.flaggedReader++
				sc.diags = append(sc.diags, sysinfoFunnelDiag(p, call, rel, name, "reader"))
			}
		}
	})
	return sc
}

func sysinfoFunnelDiag(p *Pass, call *ast.CallExpr, rel, fn, role string) Diagnostic {
	return Diagnostic{
		Rel:  rel,
		Line: p.Fset.Position(call.Pos()).Line,
		Message: ruleSysinfoViewCtxFunnel + ": " + rel + " calls sysinfo." + fn +
			" — the SystemView funnel's " + role + " is restricted to its sanctioned production callsite " +
			"(writer: runtime/bootstrap; reader: corecells/syscore systemread).",
	}
}

func runSysinfoFunnelScan(t *testing.T, writerAllow, readerAllow map[string]bool) sysinfoFunnelScan {
	t.Helper()
	var total sysinfoFunnelScan
	_ = Run(t, Production(TypedOpts{Tags: FlatNonDefaultTags()}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			sc := scanSysinfoViewFunnel(p, file, writerAllow, readerAllow)
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

func TestSysinfoViewCtxFunnel01(t *testing.T) {
	t.Parallel()
	sc := runSysinfoFunnelScan(t, sysinfoViewWriterAllowlist, sysinfoViewReaderAllowlist)
	if sc.observedWriter == 0 {
		t.Fatalf("%s: anti-vacuity — no allowlisted sysinfo.WithSystemView caller observed", ruleSysinfoViewCtxFunnel)
	}
	if sc.observedReader == 0 {
		t.Fatalf("%s: anti-vacuity — no allowlisted sysinfo.SystemViewFromContext caller observed", ruleSysinfoViewCtxFunnel)
	}
	Report(t, ruleSysinfoViewCtxFunnel, sc.diags)
}

func TestSysinfoViewCtxFunnel01_RedSelfCheck(t *testing.T) {
	t.Parallel()
	empty := map[string]bool{}
	sc := runSysinfoFunnelScan(t, empty, empty)
	if sc.flaggedWriter == 0 {
		t.Errorf("%s self-check: emptying writer allowlist flagged no sysinfo.WithSystemView caller", ruleSysinfoViewCtxFunnel)
	}
	if sc.flaggedReader == 0 {
		t.Errorf("%s self-check: emptying reader allowlist flagged no sysinfo.SystemViewFromContext caller", ruleSysinfoViewCtxFunnel)
	}
}
