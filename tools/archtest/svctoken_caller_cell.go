package archtest

// svctoken_caller_cell.go — importable SVCTOKEN-CALLER-CELL-REQUIRED-01 rule
// logic (#1632 M3).
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
// Invariant: every call expression `auth.GenerateServiceToken(...)` must
// pass a non-empty string literal as its second argument (callerCell).
// The literal must:
//   - match metadata.CellIDPattern (^[a-z][a-z0-9]+$, no dash, ≥2 chars)
//   - be a known cell ID according to cells/ directory names OR actors.yaml
//
// This gate prevents callers from omitting the caller identity or using an
// unregistered cell name, which would defeat the purpose of 4-part service
// token caller-cell propagation.
//
// Detection: type-aware — resolved via ResolvePackageRef which
// uniformly handles SelectorExpr (path A.2 qualified `auth.GenerateServiceToken`)
// and Ident (path A.3 dot-imported bare `GenerateServiceToken` after
// `import . ".../runtime/auth"`). Closes PR445-FU-PACKAGEALIASES-TYPE-AWARE-01
// + PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01 caller migration: pre-PR-TS2
// the matcher only saw SelectorExpr-shaped call sites; dot-imported bare
// `GenerateServiceToken(...)` silently slipped through.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green externally.

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// ruleSvctokenCallerCellRequired01 is the archtest rule identifier; not a credential.
//
//nolint:gosec // G101 false positive: archtest rule identifier, not a credential
const ruleSvctokenCallerCellRequired01 = "SVCTOKEN-CALLER-CELL-REQUIRED-01"

// authRuntimeImportPath is the canonical import path for runtime/auth.
// Shared with no_deleted_auth_symbols.go in the same archtest package; do
// not duplicate.
const authRuntimeImportPath = PlatformModulePath + "/runtime/auth"

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
// GenerateServiceToken callsites and appends any diagnostics to *diags,
// deduplicating by (rel, line, message) via the seen map.
func collectGenerateServiceTokenDiagsFromFile(
	p *Pass,
	file *ast.File,
	rel string,
	knownCells map[string]bool,
	seen map[string]struct{},
	diags *[]Diagnostic,
) {
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		d, bad := generateServiceTokenCallDiag(p, call, rel, knownCells)
		if !bad {
			return
		}
		key := fmt.Sprintf("%s:%d:%s", d.Rel, d.Line, d.Message)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*diags = append(*diags, d)
	})
}

// generateServiceTokenCallDiag validates a single call expression against
// SVCTOKEN-CALLER-CELL-REQUIRED-01. It returns (diag, true) when call resolves
// to auth.GenerateServiceToken AND its callerCell argument (index 1) is missing
// / non-literal / empty / pattern-malformed / unknown; otherwise (zero, false)
// for unrelated or valid calls. callerCell remains argument index 1; the
// signature is GenerateServiceToken(ring, callerCell, method, path, query, tenantID, ts).
//
// Scope: only callerCell (index 1) is statically guarded here. tenantID (index 5)
// is deliberately NOT archtest-guarded — and the typed tenant.TenantID parameter
// must not be mistaken for a Hard guard of its value. tenant.TenantID is
// `type TenantID string` (pkg/tenant/tenant_id.go) with String() returning the raw
// value, so tenant.TenantID("") and conversions of arbitrary strings remain
// expressible; the type buys call-site ergonomics (no accidental bare-string
// arg), not canonicality. Two reasons no static rule is added:
//   - There is no static path→tenant-requirement mapping, so a scanner cannot
//     decide which GenerateServiceToken call sites must sign a NON-EMPTY tenant.
//   - The tenant value is already closed at runtime, not statically:
//     (a) integrity is Hard — X-Tenant-ID is folded into the MAC unconditionally
//     (buildServiceTokenMessage), so tamper/inject/strip → MAC mismatch → 401,
//     structurally unforgeable; (b) non-emptiness for tenant-scoped internal paths
//     is Medium runtime fail-closed — configcore's handler ParseTenantID rejects
//     absent/malformed/nil-UUID X-Tenant-ID with 400.
//
// Adding a Soft archtest here (or a Medium one guarding the single existing
// tenant-scoped caller) would duplicate that runtime closure without raising the
// bar, so the runtime fail-closed is the deliberate carrier.
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
			callerCell, metadata.CellIDPattern))
	}
	if !knownCells[callerCell] {
		return mk(fmt.Sprintf(
			"auth.GenerateServiceToken callerCell %q is not a known cell ID"+
				" — register it in cells/ or actors.yaml", callerCell))
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
