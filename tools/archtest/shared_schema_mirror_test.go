// INVARIANT: SHARED-SCHEMA-MIRROR-FUNNEL-01
//
// # SHARED-SCHEMA-MIRROR-FUNNEL-01
//
// `contracts/shared/errors/error-response-v1.schema.json` is the single
// canonical source; every copy elsewhere in the repo is a codegen-managed
// mirror declared in `sharedschema.Mirrors`. An undeclared copy is an
// unauthorized fork that will drift silently — this archtest catches it.
//
// Two enforcement rules:
//
//   - A1 (reverse-enum): every `error-response-v1.schema.json` found anywhere
//     in the repo must be either the canonical file or a declared mirror
//     destination. Undeclared copies fail the test.
//
//   - A2 (caller-allowlist): `codegen.WriteOptions{..., Headerless: true, ...}`
//     composite literals must only appear in package
//     `tools/codegen/sharedschema`. The `Headerless` escape-hatch exists solely
//     so sharedschema can write JSON mirrors without the standard gocell
//     generated-file header (which would corrupt the JSON). Any other callsite
//     is forbidden — it would silently bypass the single-source funnel.
//
// AI-robust grade (funnel double-lock):
//
//   - Upstream: Medium — this A1 reverse-enum archtest detects new copies at
//     CI time; the Go type system cannot prevent creating a JSON file.
//     gh issue #<TBD-rogue-hardening> tracks Hard-ening this side (e.g. via a
//     codegen funnel that verifies at generate-time, not only at verify-time).
//
//   - Downstream: Medium — A2 caller-allowlist (archtest) restricts Headerless
//     usage; the WriteOptions struct field is exported so it cannot be sealed at
//     compile time. Same gh issue #<TBD-rogue-hardening> tracks upgrading to a
//     typed-funnel construction pattern that seals the escape-hatch.
//
// Blind-spot inventory (per ai-robust.md §"工具选定后强制盲区自检"):
//
//   - ① Rogue 4th mirror (a new copy added without declaring it in
//     sharedschema.Mirrors) in a production / example tree: covered by A1 —
//     subsetViolations helper detects the path not in the allowed set.
//     Validated by TestSharedSchemaMirror_SubsetHelper_CatchesRogue.
//     (A copy under a testdata/ tree is out of A1's scan scope — see Carve-out.)
//
//   - ② Headerless escape-hatch misused in a package other than sharedschema
//     (a caller sets Headerless: true to bypass the header guard on an
//     unrelated file write): covered by A2 — typed AST scan over production
//     packages rejects the composite literal outside the allowlist.
//     Validated by the inline allowlist check in TestSHARED_SCHEMA_MIRROR_FUNNEL_01_A2.
//
//   - ③ CI step "Verify shared-schema codegen" omitted from the verify-codegen
//     workflow job: covered by ci_pinning_test.go codegenStepNames (the step
//     name was added there as part of this PR's TDD RED wave).
//
// Carve-out:
//
//	A1 scans with ModuleScope, which excludes every testdata/ tree (the
//	scanner deliberately rejects ModuleScope + IncludeTestdata). Two classes of
//	copy therefore fall outside the A1 scan by construction:
//	  - tools/codegen/contractgen/testdata/synth/ holds intentionally divergent
//	    (snake_case) copies of error-response-v1.schema.json used as contractgen
//	    fixtures; they are NOT mirrors and must not appear in sharedschema.Mirrors.
//	  - tests/contracttest/testdata/.../error-response-v1.schema.json is a
//	    declared mirror, but it lives under testdata/; its byte-equality with the
//	    canonical source is enforced by sharedschema.Verify (the verify gate),
//	    not by A1.
//
// ref: ai-robust.md §"Funnel 双向锁评级" (Medium upstream + Medium downstream →
// Medium composite; open gh issue for Hard-ening)
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/codegen/sharedschema"
)

// schemaFileName is the well-known filename for the shared error envelope schema.
const schemaFileName = "error-response-v1.schema.json"

// TestSHARED_SCHEMA_MIRROR_FUNNEL_01_A1 (reverse-enum) scans the module's
// non-testdata trees and asserts every error-response-v1.schema.json is either:
//   - the canonical file declared in sharedschema.Mirrors' key, or
//   - a declared mirror destination (destRoot + "/" + canonicalRel).
//
// Any other path is an unauthorized copy that must either be declared in
// sharedschema.Mirrors or deleted. testdata/ trees are out of scope (see the
// package Carve-out note).
//
// Tool choice (ai-robust.md §载体决策原则): non-Go file scan → scanner
// EachContentFile over a ModuleScope. SCANNER-FRAMEWORK-USAGE-01 bans raw
// os.ReadDir / filepath.WalkDir in archtest *_test.go files. The pure subset
// predicate is tested independently in
// TestSharedSchemaMirror_SubsetHelper_CatchesRogue.
func TestSHARED_SCHEMA_MIRROR_FUNNEL_01_A1(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Build the allowed set:
	//   1. The canonical file itself.
	//   2. Each declared mirror: destRoot + "/" + canonicalRel.
	canonicalRel := schemaCanonicalRel()
	allowed := make(map[string]bool)
	allowed[canonicalRel] = true
	for canonPath, destRoots := range sharedschema.Mirrors {
		for _, destRoot := range destRoots {
			allowed[destRoot+"/"+canonPath] = true
		}
	}

	// Scan the module for every error-response schema file.
	// SCANNER-FRAMEWORK-USAGE-01: archtest *_test.go must iterate non-Go files
	// via EachContentFile, not os.ReadDir / filepath.WalkDir.
	//
	// ModuleScope excludes testdata/ trees (the scanner deliberately rejects
	// ModuleScope + IncludeTestdata). That drops BOTH the contractgen synth
	// fixtures (tools/codegen/contractgen/testdata/synth/, intentionally
	// snake_case-divergent) AND the declared contracttest testdata mirror — the
	// latter is covered by sharedschema.Verify instead. A1's role is to catch a
	// rogue *undeclared* copy in production / example trees, which is the real
	// drift surface.
	var foundRel []string
	EachContentFile(t, ModuleScope(root), []string{".json"}, func(_ *testing.T, fc ContentContext) {
		if filepath.Base(fc.Rel) != schemaFileName {
			return
		}
		rel := filepath.ToSlash(fc.Rel)
		if strings.HasPrefix(rel, "worktrees/") {
			return // peer worktrees are independent git trees (matters only when run from the main repo)
		}
		foundRel = append(foundRel, rel)
	})

	for _, v := range subsetViolations(foundRel, allowed) {
		t.Errorf("SHARED-SCHEMA-MIRROR-FUNNEL-01 A1: unauthorized copy of %s at %q —"+
			" either declare it in sharedschema.Mirrors or delete it", schemaFileName, v)
	}
}

// TestSHARED_SCHEMA_MIRROR_FUNNEL_01_A2 (caller-allowlist) asserts that every
// production composite literal `codegen.WriteOptions{..., Headerless: true, ...}`
// lives in package tools/codegen/sharedschema and nowhere else.
//
// The Headerless field is the escape-hatch that allows sharedschema to write
// JSON mirrors without the standard gocell generated-file header (which would
// corrupt the JSON). Allowing other packages to use Headerless would let them
// bypass the single-source funnel silently.
//
// Tool choice: typed AST (needs type resolution to identify codegen.WriteOptions
// struct type from possibly-aliased imports) → RunTypedProduction +
// EachInSubtree[ast.CompositeLit] + types.Info to confirm the struct type.
func TestSHARED_SCHEMA_MIRROR_FUNNEL_01_A2(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path")

	writeOptsPkgPath := modPath + "/tools/codegen"
	allowedPkg := modPath + "/tools/codegen/sharedschema"

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		// Exempt the sharedschema package itself — it is the sole sanctioned user.
		if p.Pkg != nil && p.Pkg.Path() == allowedPkg {
			return nil
		}
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
				if !isWriteOptionsType(p.TypesInfo, lit.Type, writeOptsPkgPath) {
					return
				}
				if !hasHeaderlessTrue(lit) {
					return
				}
				pos := p.Fset.Position(lit.Pos())
				ds = append(ds, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: "forbidden: codegen.WriteOptions{Headerless: true} outside " +
						"tools/codegen/sharedschema — Headerless is reserved for the shared-schema mirror funnel",
				})
			})
		}
		return ds
	})

	Report(t, "SHARED-SCHEMA-MIRROR-FUNNEL-01-A2", diags)
}

// TestSharedSchemaMirror_SubsetHelper_CatchesRogue is the blind-spot
// negative self-check for blind-spot ①.
//
// It verifies that subsetViolations correctly identifies a path that is NOT
// in the allowed set, proving A1 would catch a rogue 4th mirror.
func TestSharedSchemaMirror_SubsetHelper_CatchesRogue(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		"contracts/shared/errors/error-response-v1.schema.json":                    true,
		"examples/iotdevice/contracts/shared/errors/error-response-v1.schema.json": true,
	}
	found := []string{
		"contracts/shared/errors/error-response-v1.schema.json",
		"examples/iotdevice/contracts/shared/errors/error-response-v1.schema.json",
		"runtime/http/error-response-v1.schema.json", // rogue: not in allowed set
	}
	violations := subsetViolations(found, allowed)
	if len(violations) == 0 {
		t.Error("subsetViolations must report the rogue path; got no violations")
	}
	foundRogue := false
	for _, v := range violations {
		if v == "runtime/http/error-response-v1.schema.json" {
			foundRogue = true
		}
	}
	if !foundRogue {
		t.Errorf("expected rogue path in violations, got: %v", violations)
	}
}

// ─── pure helpers ────────────────────────────────────────────────────────────

// schemaCanonicalRel returns the canonical module-relative slash path of the
// single source-of-truth schema file.  The value is derived from
// sharedschema.Mirrors' key rather than hard-coded here to preserve single-
// source semantics: if the canonical path ever moves, only sharedschema.Mirrors
// needs updating.
func schemaCanonicalRel() string {
	for k := range sharedschema.Mirrors {
		return k
	}
	// Fallback: Mirrors is empty (Wave 2 not yet deployed). Use the known path.
	return "contracts/shared/errors/error-response-v1.schema.json"
}

// subsetViolations returns every element of found that is NOT present in
// allowed. A non-empty return slice means found contains elements outside the
// declared set.  This is a pure function so it can be unit-tested in isolation
// (TestSharedSchemaMirror_SubsetHelper_CatchesRogue).
func subsetViolations(found []string, allowed map[string]bool) []string {
	var bad []string
	for _, f := range found {
		if !allowed[f] {
			bad = append(bad, f)
		}
	}
	return bad
}

// isWriteOptionsType returns true when expr resolves to codegen.WriteOptions
// from the given writeOptsPkgPath.
func isWriteOptionsType(info *types.Info, expr ast.Expr, writeOptsPkgPath string) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == writeOptsPkgPath && obj.Name() == "WriteOptions"
}

// hasHeaderlessTrue returns true when the CompositeLit contains a key-value
// element `Headerless: true`. SCANNER-FRAMEWORK-USAGE-01: direct-child AST
// iteration goes through EachInChildren, not a for-range over lit.Elts.
func hasHeaderlessTrue(lit *ast.CompositeLit) bool {
	var found bool
	EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		ident, ok := kv.Key.(*ast.Ident)
		if !ok || ident.Name != "Headerless" {
			return
		}
		// Value must be the boolean literal `true`.
		if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "true" {
			found = true
		}
	})
	return found
}
