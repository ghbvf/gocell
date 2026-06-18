package archtest

// cell_transport_docontract_caller.go — importable CELL-TRANSPORT-DOCONTRACT-CALLER-01
// rule logic (Epic #1423 US4 follow-up #2093). Non-test home so the detector core
// can be shared by the production scan, the synthetic-detector table, and the RED
// fixture scan (single source, no parallel rule body) — mirrors
// cell_sync_transport_funnel.go.

import (
	"fmt"
	"go/ast"
)

// ruleCellTransportDoContractCaller is the archtest rule identifier.
const ruleCellTransportDoContractCaller = "CELL-TRANSPORT-DOCONTRACT-CALLER-01"

// doContractTransportPkgPath is the canonical import path of the sync transport
// seam package that declares CellTransport.DoContract. (Named distinctly from the
// archtest-tagged transportPkgPath in inprocess_bind_authority_funnel_test.go so
// this non-tagged detector file compiles in the default build.)
//
// Anchored to PlatformFrameworkModulePath so a module rename updates one place; if
// the runtime/transport package itself is relocated, this constant must move with
// it (a documented Go ceiling for path-anchored archtest rules, same as
// COMMAND-ASYNC-EMIT-CALLER-01's package anchors). A stale path silently turns the
// scan green — the synthetic fixture (FixtureScanRED) is the anti-vacuity guard.
const doContractTransportPkgPath = PlatformFrameworkModulePath + "/runtime/transport"

// doContractMethodName is the sole sealed cross-cell sync dispatch method.
const doContractMethodName = "DoContract"

// forbiddenDoContractRef classifies a resolved (pkgPath, methodName) pair as the
// transport.CellTransport.DoContract method. It returns true only for the
// DoContract method declared by the runtime/transport package — the dispatch
// surface a cell must NOT call directly. Pure detector core shared by the
// production scan and the synthetic table (mirrors forbiddenHTTPClientRef).
//
// Rationale (epic #1423 US4 follow-up #2093 — generated-client downstream Hard):
// once the codegen-generated contract client lands as the SOLE sealed sibling-cell
// call type, a cell must reach a sibling's http contract ONLY through that
// generated client (which holds the sealed transport.CellTransport and calls
// DoContract internally). A cell calling transport.CellTransport.DoContract
// directly with a hand-built request bypasses the generated client — and would
// NOT trip CELL-SYNC-TRANSPORT-FUNNEL-01 (that scan bans raw net/http clients, not
// DoContract). This is the second Medium backstop that closes the funnel: together
// with the net/http ban, the only expressible cell→sibling sync path is a
// generated client. The sanctioned DoContract callers live under
// generated/contracts/** (not cell-owned packages), so the cell-scoped production
// scan excludes them by construction.
func forbiddenDoContractRef(pkgPath, methodName string) bool {
	return pkgPath == doContractTransportPkgPath && methodName == doContractMethodName
}

// scanCellDoContractCall walks one file's AST for method-call selectors and emits
// a Diagnostic for every direct transport.CellTransport.DoContract call. The
// caller decides whether the file belongs to a cell (production scan) or not
// (fixture scan). Method resolution goes through ResolveMethodCall (info.Selections),
// which handles interface/direct/pointer/alias receivers, so the receiver may be
// the CellTransport interface or a concrete sealed impl.
func scanCellDoContractCall(p *Pass, file *ast.File, rel string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn == nil || fn.Pkg() == nil {
			return
		}
		// fn.Pkg() is the package that DECLARES the method, so the path check pins
		// the resolved func to runtime/transport's DoContract — an unrelated
		// package's same-named method (different declaring pkg) does not match.
		if !forbiddenDoContractRef(fn.Pkg().Path(), fn.Name()) {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"%s: cell production code calls transport.CellTransport.DoContract directly at %s:%d — a cell "+
					"must reach a sibling cell's http contract through the codegen-generated contract client "+
					"(generated/contracts/**), the sole sealed sibling-call type, not by calling DoContract with a "+
					"hand-built request (epic #1423 US4 follow-up #2093).",
				ruleCellTransportDoContractCaller, rel, line),
		})
	})
	return diags
}
