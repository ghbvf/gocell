package archtest

// svctoken_caller_cell.go — importable SVCTOKEN-CALLER-CELL-REQUIRED-01 rule
// logic (#1632 M3, extended #1966 T042).
//
// This is the non-test home of the SVCTOKEN-CALLER-CELL-REQUIRED-01 scanner so
// it can be compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in a
// _test.go file). GoCell's own TestSVCTOKEN_CALLER_CELL_REQUIRED_01 in
// svctoken_caller_cell_test.go calls the same CheckSvctokenCallerCellRequired01
// — single source, no parallel rule body.
//
// # SVCTOKEN-CALLER-CELL-REQUIRED-01
//
// Three invariants guarded jointly:
//
// A. Every call expression `auth.GenerateServiceToken(...)` must pass a
//    non-empty string literal as its second argument (callerCell). The literal
//    must match metadata.CellIDPattern (^[a-z][a-z0-9]+$, no dash, ≥2 chars)
//    and be a known cell ID (cells/ or actors.yaml).
//
//    Updated signature (#1966 T042):
//    GenerateServiceToken(ring, callerCell, method, path, query, tenantID, principalHeader, ts)
//    callerCell is STILL argument index 1.
//
// B. Every call expression `auth.SignInternalRequest(...)` must pass a
//    non-empty string literal as its third argument (index 2, callerCell).
//    The same cell-ID constraints as A apply. SignInternalRequest is the
//    single sanctioned production funnel for signing internal requests.
//
// C. Production code outside runtime/auth must NOT call auth.GenerateServiceToken
//    directly. The sole legal direct callers are:
//      - the runtime/auth package itself (SignInternalRequest is the sanctioned
//        funnel that wraps it), and
//      - _test.go files (integration / unit tests may call it for test fixtures).
//    Any non-test, non-auth production package calling GenerateServiceToken
//    directly bypasses the SignInternalRequest funnel and is rejected here.
//
// Detection: type-aware — resolved via ResolvePackageRef which uniformly
// handles SelectorExpr (qualified) and Ident (dot-imported). Closes
// PR445-FU-PACKAGEALIASES-TYPE-AWARE-01 + PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green externally.

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// ruleSvctokenCallerCellRequired01 is the archtest rule identifier; not a credential.
const ruleSvctokenCallerCellRequired01 = "SVCTOKEN-CALLER-CELL-REQUIRED-01"

// authRuntimeImportPath is the canonical import path for runtime/auth.
// Shared with no_deleted_auth_symbols.go in the same archtest package; do
// not duplicate.
const authRuntimeImportPath = PlatformFrameworkModulePath + "/runtime/auth"

// CheckSvctokenCallerCellRequired01 runs SVCTOKEN-CALLER-CELL-REQUIRED-01 over
// the running module and returns its diagnostics: every call to
// auth.GenerateServiceToken must pass a valid cell-ID string literal as its
// second argument (callerCell). It is the importable rule body; GoCell's
// TestSVCTOKEN_CALLER_CELL_REQUIRED_01 dogfoods it directly — single source, no
// parallel rule body.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green
// externally.
func CheckSvctokenCallerCellRequired01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)
	knownCells := discoverKnownCells(t, root)

	// tests=true loads the test variants of every package, so test helpers
	// (e.g. examples/ssobff/walkthrough_test.go) that call
	// GenerateServiceToken are also scanned. FlatNonDefaultTags()
	// returns the union of every tag tracked in KnownNonDefaultTags(); a
	// single Run(t, Production(...)) call carrying all tags simultaneously satisfies
	// every //go:build constraint at once (e.g. `//go:build integration` is
	// included because `integration` is in the set; `//go:build integration
	// && otelcollector` is included because both tags are present). This
	// closes PR445-FU finding F2 (the prior nil-tags call silently skipped
	// integration / e2e / examples_smoke files).
	//
	// Single-load avoids retaining 7 independent type graphs in
	// SharedResolver's cache (one per tag combination), which OOM'd CI
	// runners with ~7GB RAM. The flat-load is functionally equivalent for
	// per-callsite rules; downstream callers that need per-tag-set
	// disposition can iterate KnownNonDefaultTags() themselves with
	// post-load filtering by file build constraints.
	seen := map[string]struct{}{}
	var diags []Diagnostic
	_ = Run(t, Production(TypedOpts{Tests: true, Tags: cfg.BuildTags}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			authOwningPackage := p.Pkg.Path() == authRuntimeImportPath
			for _, file := range p.Files {
				rel := p.Rel(file)
				if authOwningPackage && strings.HasSuffix(rel, "_test.go") {
					continue
				}
				collectGenerateServiceTokenDiagsFromFile(p, file, rel, knownCells, seen, &diags)
			}
			return nil
		})

	return diags
}

// collectGenerateServiceTokenDiagsFromFile walks a single file's AST for
// GenerateServiceToken and SignInternalRequest callsites and appends any
// diagnostics to *diags, deduplicating by (rel, line, message) via the seen map.
//
// # Function-level carve-out for SignInternalRequest body (#1966 T042)
//
// The body of auth.SignInternalRequest contains exactly one direct call to
// auth.GenerateServiceToken. In that call, callerCell (index 1) is a forwarded
// parameter — not a string literal — because SignInternalRequest is the single
// sanctioned funnel whose own callerCell (index 2) is already locked to a
// literal by B-arm (signInternalRequestCallDiag). Requiring a literal at the
// forwarding site would force a redundant constant wrapper with no safety
// benefit; the funnel itself provides the invariant.
//
// The carve-out is function-level only (not file-level, per ai-robust.md
// §Carve-out): A-arm is skipped only for GenerateServiceToken calls whose
// enclosing top-level function is named "SignInternalRequest" in the auth
// package. All other GenerateServiceToken calls — in any file or package —
// remain subject to the literal callerCell requirement.
func collectGenerateServiceTokenDiagsFromFile(
	p *Pass,
	file *ast.File,
	rel string,
	knownCells map[string]bool,
	seen map[string]struct{},
	diags *[]Diagnostic,
) {
	appendIfBad := func(d Diagnostic, bad bool) {
		if !bad {
			return
		}
		key := fmt.Sprintf("%s:%d:%s", d.Rel, d.Line, d.Message)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*diags = append(*diags, d)
	}
	authOwningPackage := p.Pkg.Path() == authRuntimeImportPath
	// Walk each top-level FuncDecl so we know the enclosing function name for
	// the function-level carve-out (see godoc above).
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		// inSignInternalRequestFunnel is true when this FuncDecl is the
		// auth.SignInternalRequest implementation itself.  GenerateServiceToken
		// calls inside it are the sanctioned single-funnel forwarding call;
		// callerCell is a forwarded parameter, not a literal, and is already
		// literal-locked at every SignInternalRequest callsite by B-arm.
		inSignInternalRequestFunnel := authOwningPackage &&
			fd.Name != nil && fd.Name.Name == "SignInternalRequest"

		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if !inSignInternalRequestFunnel {
				appendIfBad(generateServiceTokenCallDiag(p, call, rel, knownCells))
				// C-arm: ban direct GenerateServiceToken calls from production
				// code outside runtime/auth (non-test files only; _test.go files
				// are implicitly exempted by the !authOwningPackage check combined
				// with the _test.go suffix guard at the file level above, but we
				// guard explicitly here for clarity).
				if !authOwningPackage && !strings.HasSuffix(rel, "_test.go") {
					appendIfBad(generateServiceTokenDirectCallBanDiag(p, call, rel))
				}
			}
			appendIfBad(signInternalRequestCallDiag(p, call, rel, knownCells))
		})
	})
}

// generateServiceTokenCallDiag validates a single call expression against
// SVCTOKEN-CALLER-CELL-REQUIRED-01. It returns (diag, true) when call resolves
// to auth.GenerateServiceToken AND its callerCell argument (index 1) is missing
// / non-literal / empty / pattern-malformed / unknown; otherwise (zero, false)
// for unrelated or valid calls. callerCell remains argument index 1; the
// updated signature is:
// GenerateServiceToken(ring, callerCell, method, path, query, tenantID, principalHeader, ts).
//
// Scope: only callerCell (index 1) is statically guarded here. tenantID (index 5)
// and principalHeader (index 6) are deliberately NOT archtest-guarded: their
// values are runtime-derived (tenant from context, principalHeader from
// encodePrincipalHeader), and their integrity is Hard — both are folded into the
// MAC unconditionally (buildServiceTokenMessage), so tamper/inject/strip →
// MAC mismatch → 401, structurally unforgeable.
func generateServiceTokenCallDiag(p *Pass, call *ast.CallExpr, rel string, knownCells map[string]bool) (Diagnostic, bool) {
	path, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok || path != authRuntimeImportPath || name != "GenerateServiceToken" {
		return Diagnostic{}, false
	}
	line := p.Fset.Position(call.Pos()).Line
	mk := func(msg string) (Diagnostic, bool) {
		return Diagnostic{Rel: rel, Line: line, Message: msg}, true
	}

	if len(call.Args) < 2 {
		return mk("auth.GenerateServiceToken called with fewer than 2 arguments — missing callerCell")
	}
	lit, isLit := call.Args[1].(*ast.BasicLit)
	if !isLit {
		return mk("auth.GenerateServiceToken second argument (callerCell) must be a string literal")
	}
	callerCell, ok := StringLitValue(lit)
	if !ok {
		return mk("auth.GenerateServiceToken second argument (callerCell) must be a string literal")
	}
	if callerCell == "" {
		return mk("auth.GenerateServiceToken callerCell must not be empty")
	}
	if !metadata.MatchCellID(callerCell) {
		return mk(fmt.Sprintf(
			"auth.GenerateServiceToken callerCell %q does not match metadata.CellIDPattern (%s)",
			callerCell, metadata.CellIDPattern,
		))
	}
	if !knownCells[callerCell] {
		return mk(fmt.Sprintf(
			"auth.GenerateServiceToken callerCell %q is not a known cell ID"+
				" — register it in cells/ or actors.yaml", callerCell,
		))
	}
	return Diagnostic{}, false
}

// generateServiceTokenDirectCallBanDiag enforces invariant C of
// SVCTOKEN-CALLER-CELL-REQUIRED-01: production code outside runtime/auth must
// not call auth.GenerateServiceToken directly. It returns (diag, true) when
// the call resolves to auth.GenerateServiceToken and the call is in a
// non-auth, non-test file. Returns (zero, false) for unrelated calls, calls
// inside runtime/auth, or calls in _test.go files.
//
// Callers must only invoke this arm for files where authOwningPackage is false
// and rel does not end in _test.go (both checked by collectGenerateServiceTokenDiagsFromFile
// before dispatching here, so this function itself does not re-check to avoid
// redundancy).
func generateServiceTokenDirectCallBanDiag(p *Pass, call *ast.CallExpr, rel string) (Diagnostic, bool) {
	path, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok || path != authRuntimeImportPath || name != "GenerateServiceToken" {
		return Diagnostic{}, false
	}
	line := p.Fset.Position(call.Pos()).Line
	return Diagnostic{
		Rel:  rel,
		Line: line,
		Message: "production code outside runtime/auth must not call auth.GenerateServiceToken " +
			"directly — use auth.SignInternalRequest (the sanctioned signing funnel) " +
			"(SVCTOKEN-CALLER-CELL-REQUIRED-01 C-arm)",
	}, true
}

// signInternalRequestCallDiag validates a single call expression against
// SVCTOKEN-CALLER-CELL-REQUIRED-01 for the SignInternalRequest funnel. It
// returns (diag, true) when the call resolves to auth.SignInternalRequest AND
// its callerCell argument (index 2) is missing / non-literal / empty /
// pattern-malformed / unknown; otherwise (zero, false) for unrelated or valid
// calls.
//
// Signature: SignInternalRequest(ctx, ring, callerCell, req, tenantID, clock)
// callerCell is argument index 2 (third argument, 0-indexed).
//
// This guard ensures that the single sanctioned production funnel for signing
// internal requests carries a valid, registered cell ID. Production code
// outside runtime/auth must use SignInternalRequest; GenerateServiceToken
// is reserved for auth-internal use and tests.
func signInternalRequestCallDiag(p *Pass, call *ast.CallExpr, rel string, knownCells map[string]bool) (Diagnostic, bool) {
	path, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok || path != authRuntimeImportPath || name != "SignInternalRequest" {
		return Diagnostic{}, false
	}
	line := p.Fset.Position(call.Pos()).Line
	mk := func(msg string) (Diagnostic, bool) {
		return Diagnostic{Rel: rel, Line: line, Message: msg}, true
	}

	const callerCellIdx = 2 // callerCell is the 3rd positional arg of SignInternalRequest
	if len(call.Args) <= callerCellIdx {
		return mk("auth.SignInternalRequest called with too few arguments — missing callerCell (index 2)")
	}
	lit, isLit := call.Args[callerCellIdx].(*ast.BasicLit)
	if !isLit {
		return mk("auth.SignInternalRequest third argument (callerCell) must be a string literal")
	}
	callerCell, ok := StringLitValue(lit)
	if !ok {
		return mk("auth.SignInternalRequest third argument (callerCell) must be a string literal")
	}
	if callerCell == "" {
		return mk("auth.SignInternalRequest callerCell must not be empty")
	}
	if !metadata.MatchCellID(callerCell) {
		return mk(fmt.Sprintf(
			"auth.SignInternalRequest callerCell %q does not match metadata.CellIDPattern (%s)",
			callerCell, metadata.CellIDPattern,
		))
	}
	if !knownCells[callerCell] {
		return mk(fmt.Sprintf(
			"auth.SignInternalRequest callerCell %q is not a known cell ID"+
				" — register it in cells/ or actors.yaml", callerCell,
		))
	}
	return Diagnostic{}, false
}

// discoverKnownCells returns the set of valid caller cell IDs from
// ProjectMeta.Cells (covers both top-level and examples cells via
// metadata path-pattern matching) plus actor IDs from actors.yaml.
func discoverKnownCells(t *testing.T, root string) map[string]bool {
	t.Helper()
	known := map[string]bool{}

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	for id := range project.Cells {
		if metadata.MatchCellID(id) {
			known[id] = true
		}
	}
	for _, a := range project.Actors {
		if metadata.MatchCellID(a.ID) {
			known[a.ID] = true
		}
	}
	return known
}
