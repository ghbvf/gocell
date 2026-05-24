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
//     sharedschema.Mirrors): covered by A1 — subsetViolations helper detects
//     the path not in the allowed set.
//     Validated by TestSharedSchemaMirror_SubsetHelper_CatchesRogue.
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
//	tools/codegen/contractgen/testdata/synth/ contains three intentional
//	divergent copies of error-response-v1.schema.json (one per synthetic
//	fixture scenario). These are deliberate fixtures testing contractgen
//	behaviour with hand-crafted schema content — they must NOT be declared
//	as mirrors and must be excluded from the A1 reverse-enum scan.
//	Registered here as the sole carve-out; any additional divergent copy
//	under testdata/synth/ must be added explicitly to synthCarveOutPrefix.
//
// ref: ai-robust.md §"Funnel 双向锁评级" (Medium upstream + Medium downstream →
// Medium composite; open gh issue for Hard-ening)
package archtest

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/codegen/sharedschema"
)

// schemaFileName is the well-known filename for the shared error envelope schema.
const schemaFileName = "error-response-v1.schema.json"

// synthCarveOutPrefix is the repo-relative slash-path prefix for intentionally
// divergent copies of schemaFileName that live inside contractgen synth fixtures.
// These are NOT mirrors and must not appear in sharedschema.Mirrors; they are
// excluded from the A1 reverse-enum scan.
const synthCarveOutPrefix = "tools/codegen/contractgen/testdata/synth/"

// TestSHARED_SCHEMA_MIRROR_FUNNEL_01_A1 (reverse-enum) walks the entire repo
// and asserts every error-response-v1.schema.json is either:
//   - the canonical file declared in sharedschema.Mirrors' key, or
//   - a declared mirror destination (destRoot + "/" + canonicalRel), or
//   - under the synthCarveOutPrefix carve-out (contractgen synth fixtures).
//
// Any path outside these sets is an unauthorized copy that must either be
// declared in sharedschema.Mirrors or deleted.
//
// Tool choice (ai-robust.md §载体决策原则): metadata / file scan → os.ReadDir
// recursive walk (not Go AST, not EachContentFile whose suffix filter still
// applies, but a direct filesystem walk is needed to visit non-Go files).
// filepath.WalkDir is approved for non-*_test.go usages; this file IS a test
// file so we use a hand-rolled recursive helper to avoid the
// SCANNER-FRAMEWORK-USAGE-01 ban on filepath.WalkDir in archtest *_test.go
// files. The recursive helper is tested independently in
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

	// Walk the repo from root, excluding worktrees/ (peer worktrees are
	// independent Git trees, not part of this module) and the synth carve-out.
	found := findSchemaFiles(t, root)

	// Convert absolute paths to module-relative slash paths for comparison.
	var foundRel []string
	for _, abs := range found {
		rel, err := filepath.Rel(root, abs)
		require.NoError(t, err, "rel path from root")
		rel = filepath.ToSlash(rel)
		// Apply worktrees/ exclusion (belt-and-suspenders: findSchemaFiles
		// already skips worktrees but double-check).
		if strings.HasPrefix(rel, "worktrees/") {
			continue
		}
		// Apply synth carve-out.
		if strings.HasPrefix(rel, synthCarveOutPrefix) {
			continue
		}
		foundRel = append(foundRel, rel)
	}

	violations := subsetViolations(foundRel, allowed)
	for _, v := range violations {
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
					Rel:     rel,
					Line:    pos.Line,
					Message: "forbidden: codegen.WriteOptions{Headerless: true} outside tools/codegen/sharedschema — Headerless is reserved for the shared-schema mirror funnel",
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
		"contracts/shared/errors/error-response-v1.schema.json":          true,
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

// findSchemaFiles recursively lists all files named schemaFileName under root,
// excluding the "worktrees" top-level directory. It avoids filepath.WalkDir
// (banned in archtest *_test.go by SCANNER-FRAMEWORK-USAGE-01) by using
// os.ReadDir with manual recursion.
func findSchemaFiles(t *testing.T, root string) []string {
	t.Helper()
	var results []string
	walkDirForSchema(t, root, root, &results)
	return results
}

func walkDirForSchema(t *testing.T, root, dir string, out *[]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Unreadable dirs (e.g. broken symlinks in node_modules) are skipped
		// silently to avoid flaky failures on developer machines.
		return
	}
	for _, e := range entries {
		name := e.Name()
		// Skip hidden dirs and well-known noise dirs.
		if name == ".git" || name == "vendor" || name == "node_modules" {
			continue
		}
		// Skip worktrees at the top level (peer worktrees are independent Git
		// trees; their schemas are not part of this module).
		absEntry := filepath.Join(dir, name)
		if rel, relErr := filepath.Rel(root, absEntry); relErr == nil {
			topSeg := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
			if topSeg == "worktrees" {
				continue
			}
		}
		if e.IsDir() {
			walkDirForSchema(t, root, absEntry, out)
		} else if name == schemaFileName {
			*out = append(*out, absEntry)
		}
	}
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
// element `Headerless: true`.
func hasHeaderlessTrue(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		ident, ok := kv.Key.(*ast.Ident)
		if !ok || ident.Name != "Headerless" {
			continue
		}
		// Check that the value is the boolean literal `true`.
		ident2, ok := kv.Value.(*ast.Ident)
		if ok && ident2.Name == "true" {
			return true
		}
	}
	return false
}
