package archtest

// jwt_claims_no_authz_epoch_test.go — Hard guard that the access JWT claim
// payload does not carry `authz_epoch` anywhere in the codebase.
//
// INVARIANT: JWT-CLAIMS-NO-AUTHZ-EPOCH-01
//
// AI-rebust grade: Hard (form uniqueness via four independent AST anchors
// covering the field declaration, the decoder normalization, the mint-path
// literal, and the struct-tag JSON key). The rule has four production-side
// prongs plus two RED fixtures:
//
//  1. `kernel/cell.Claims` (declared in kernel/cell/auth_types.go) must NOT
//     contain a field named `AuthzEpoch`. Catches regression at the source
//     of truth.
//
//  2. `runtime/auth.standardClaims` map (the JWT decoder's known-key set)
//     must NOT contain the literal string "authz_epoch". Re-introduction
//     would silently absorb a stray epoch claim into the normalized Claims,
//     undermining (1)'s detectability via Extra.
//
//  3. runtime/auth/jwt.go body must contain NO `*ast.BasicLit` with value
//     "authz_epoch" outside the standardClaims composite literal. This
//     blocks any literal map write like `m["authz_epoch"] = ...` and any
//     log line referencing the dead claim name.
//
//  3b. kernel/cell/auth_types.go must contain NO struct field whose JSON
//      tag key equals "authz_epoch". This closes the bypass form:
//      `Epoch int64 \`json:"authz_epoch"\`` has field name "Epoch" (missed
//      by prong 1) and its tag BasicLit.Value is the full raw string
//      ` + "`" + "`json:\"authz_epoch\"`" + "`" + ` — NOT the bare literal "authz_epoch"
//      (missed by the old prong 3 bare-literal scan). scanner.StructTagJSONKey
//      normalises the tag via reflect.StructTag.Get("json") and correctly
//      detects this form.
//
//  4. Two RED fixtures:
//     - testdata/jwt_claims_with_authz_epoch_red/claims.go: AuthzEpoch field
//       + bare literal "authz_epoch" (prongs 1+3 forms).
//     - testdata/jwt_claims_with_authz_epoch_tag_only_red/claims.go: field
//       named "Epoch" with `json:"authz_epoch"` tag (prong 3b form; bypasses
//       prong 1 and the old prong 3 bare-literal scan).
//
// Blind-spot disclosure (ai-collab.md §"工具选定后强制盲区自检"):
//   - field rename to a JSON-tagged sibling (e.g. `Epoch int64 \`json:"authz_epoch"\``)
//     would bypass prong 1. It is now caught by prong 3b (StructTagJSONKey
//     parses the raw-string tag and extracts the JSON key). Previously this
//     form evaded detection; the tag-only RED fixture proves the fix.
//   - struct embed: if a future `Claims` embeds a sibling with AuthzEpoch,
//     the field appears promoted. Prong 1 reads only direct fields — this is
//     intentional; promoted fields cannot reach wire serialization without
//     mint-path support (covered by prong 3). Prong 3b similarly covers
//     only direct fields' tags; embedded struct tags on the embedded struct
//     itself are checked when that struct's file is scanned via prong 3b
//     targeting auth_types.go.
//   - dynamic claim writes via reflect.Value.SetMapIndex would bypass the
//     literal-string scan. The auth package does not use reflect for mint;
//     if introduced, extend prong 3 to also lint SetMapIndex callers.
//   - dynamically-constructed struct tags (reflect.StructTag at runtime)
//     are AST-invisible. Asserted absent by
//     TestJWTClaimsNoAuthzEpoch_BlindSpot_DynamicStructTag.
//   - cell.Claims is a type alias from runtime/auth.Claims (per godoc).
//     The single source of truth is kernel/cell — checking that struct is
//     sufficient.

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	authzEpochClaimKey             = "authz_epoch"
	authzEpochFieldName            = "AuthzEpoch"
	authzEpochClaimsFile           = "kernel/cell/auth_types.go"
	authzEpochClaimsType           = "Claims"
	authzEpochStdMapVar            = "standardClaims"
	authzEpochStdMapFile           = "runtime/auth/jwt.go"
	authzEpochJWTFile              = "runtime/auth/jwt.go"
	authzEpochRedFixtureRel        = "tools/archtest/testdata/jwt_claims_with_authz_epoch_red/claims.go"
	authzEpochTagOnlyRedFixtureRel = "tools/archtest/testdata/jwt_claims_with_authz_epoch_tag_only_red/claims.go"
)

// TestJWTClaimsNoAuthzEpoch_StructNoField (prong 1).
func TestJWTClaimsNoAuthzEpoch_StructNoField(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, authzEpochClaimsFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "JWT-CLAIMS-NO-AUTHZ-EPOCH-01: parse %s", authzEpochClaimsFile)

	st := findStructTypeForTypeDecl(file, authzEpochClaimsType)
	require.NotNilf(t, st,
		"JWT-CLAIMS-NO-AUTHZ-EPOCH-01: type %q not found as struct in %s — was it renamed?",
		authzEpochClaimsType, authzEpochClaimsFile)

	hasField := false
	if st.Fields != nil {
		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				if name != nil && name.Name == authzEpochFieldName {
					hasField = true
				}
			}
		}
	}
	assert.Falsef(t, hasField,
		"JWT-CLAIMS-NO-AUTHZ-EPOCH-01: %s.%s must not declare field %q. "+
			"S4d removed JWT epoch provenance; epoch lives on session/refresh row. "+
			"Re-introducing the field regresses ADR §A8.",
		authzEpochClaimsFile, authzEpochClaimsType, authzEpochFieldName)
}

// TestJWTClaimsNoAuthzEpoch_StandardClaimsNoKey (prong 2).
func TestJWTClaimsNoAuthzEpoch_StandardClaimsNoKey(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, authzEpochStdMapFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "JWT-CLAIMS-NO-AUTHZ-EPOCH-01: parse %s", authzEpochStdMapFile)

	cl := findCompositeLitForVar(file, authzEpochStdMapVar)
	require.NotNilf(t, cl,
		"JWT-CLAIMS-NO-AUTHZ-EPOCH-01: composite literal for var %q not found in %s",
		authzEpochStdMapVar, authzEpochStdMapFile)

	found := false
	scanner.EachInSubtree[ast.BasicLit](cl, func(bl *ast.BasicLit) {
		if bl.Kind == gotoken.STRING && stripBackticksOrQuotes(bl.Value) == authzEpochClaimKey {
			found = true
		}
	})
	assert.Falsef(t, found,
		"JWT-CLAIMS-NO-AUTHZ-EPOCH-01: %s.%s must not contain key %q. "+
			"Including the key re-absorbs stray legacy tokens into Claims and hides the "+
			"regression from TestRefresh_AccessJWT_NoAuthzEpochClaim.",
		authzEpochStdMapFile, authzEpochStdMapVar, authzEpochClaimKey)
}

// TestJWTClaimsNoAuthzEpoch_MintPathNoLiteral (prong 3).
func TestJWTClaimsNoAuthzEpoch_MintPathNoLiteral(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, authzEpochJWTFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "JWT-CLAIMS-NO-AUTHZ-EPOCH-01: parse %s", authzEpochJWTFile)

	standardClaimsLit := findCompositeLitForVar(file, authzEpochStdMapVar)

	var hits []string
	scanner.EachInSubtree[ast.BasicLit](file, func(bl *ast.BasicLit) {
		if bl.Kind != gotoken.STRING || stripBackticksOrQuotes(bl.Value) != authzEpochClaimKey {
			return
		}
		if standardClaimsLit != nil && nodeContains(standardClaimsLit, bl) {
			return
		}
		hits = append(hits, fset.Position(bl.Pos()).String())
	})
	assert.Emptyf(t, hits,
		"JWT-CLAIMS-NO-AUTHZ-EPOCH-01: literal %q appears in %s outside standardClaims. "+
			"S4d mint path must not write the claim; row-SoR replaces it (ADR §A8). Hits: %v",
		authzEpochClaimKey, authzEpochJWTFile, hits)
}

// TestJWTClaimsNoAuthzEpoch_ClaimsStructNoTagKey (prong 3b) — scans every
// struct field in kernel/cell/auth_types.go and asserts that no field carries
// a JSON tag whose key is "authz_epoch".
//
// This closes the bypass form that prong 1 and the old prong 3 miss:
//
//	Epoch int64 `json:"authz_epoch"`
//
// Prong 1 only checks the Go field NAME (AuthzEpoch); "Epoch" passes. The old
// prong 3 scans for the bare literal "authz_epoch" — but a struct tag is a
// raw-string BasicLit whose Value is "`json:\"authz_epoch\"`", NOT the bare
// key. scanner.StructTagJSONKey normalises via reflect.StructTag.Get("json")
// and detects the key regardless of field name.
//
// Blind-spot: dynamically-constructed struct tags (reflect.StructTag set at
// runtime) are AST-invisible. Asserted absent by
// TestJWTClaimsNoAuthzEpoch_BlindSpot_DynamicStructTag.
func TestJWTClaimsNoAuthzEpoch_ClaimsStructNoTagKey(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, authzEpochClaimsFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "JWT-CLAIMS-NO-AUTHZ-EPOCH-01: parse %s", authzEpochClaimsFile)

	var hits []string
	scanner.EachInSubtree[ast.Field](file, func(f *ast.Field) {
		if scanner.StructTagJSONKey(f.Tag, authzEpochClaimKey) {
			hits = append(hits, fset.Position(f.Pos()).String())
		}
	})
	assert.Emptyf(t, hits,
		"JWT-CLAIMS-NO-AUTHZ-EPOCH-01 (prong 3b): %s must not contain a struct field "+
			"with JSON tag key %q. S4d removed JWT epoch provenance; epoch lives on "+
			"session/refresh row. Even a renamed field like `Epoch int64 `+\"`\"+`json:%q`+\"`\"+`` "+
			"would carry the claim on the wire and regress ADR §A8. Hits: %v",
		authzEpochClaimsFile, authzEpochClaimKey, authzEpochClaimKey, hits)
}

// TestJWTClaimsNoAuthzEpoch_TagOnlyRedFixtureDetected proves prong 3b catches
// the tag-only bypass form: a field named "Epoch" (not "AuthzEpoch") with tag
// `json:"authz_epoch"`. Prong 1 misses this (wrong field name); the old prong 3
// missed it (tag BasicLit.Value is the full raw string, not the bare key).
func TestJWTClaimsNoAuthzEpoch_TagOnlyRedFixtureDetected(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, authzEpochTagOnlyRedFixtureRel)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "JWT-CLAIMS-NO-AUTHZ-EPOCH-01: parse tag-only RED fixture %s", authzEpochTagOnlyRedFixtureRel)

	// Prong 1 must NOT detect AuthzEpoch field name (proving the bypass).
	fieldNameDetected := false
	scanner.EachInSubtree[ast.StructType](file, func(st *ast.StructType) {
		if st.Fields == nil {
			return
		}
		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				if name != nil && name.Name == authzEpochFieldName {
					fieldNameDetected = true
				}
			}
		}
	})
	assert.Falsef(t, fieldNameDetected,
		"tag-only RED fixture self-check: %s must NOT declare a field named %q "+
			"(we are testing the bypass form where field name != AuthzEpoch)",
		authzEpochTagOnlyRedFixtureRel, authzEpochFieldName)

	// Prong 3b MUST detect the tag-based form.
	tagDetected := false
	scanner.EachInSubtree[ast.Field](file, func(f *ast.Field) {
		if scanner.StructTagJSONKey(f.Tag, authzEpochClaimKey) {
			tagDetected = true
		}
	})
	assert.Truef(t, tagDetected,
		"tag-only RED fixture self-check: %s must contain a field with JSON tag key %q "+
			"so prong 3b detects it. Check that the fixture has `Epoch int64 `+\"`\"+`json:%q`+\"`\"+``.",
		authzEpochTagOnlyRedFixtureRel, authzEpochClaimKey, authzEpochClaimKey)
}

// TestJWTClaimsNoAuthzEpoch_BlindSpot_DynamicStructTag asserts that no code
// in kernel/cell/ uses reflect.StructTag assignment or reflect.Value.Set on
// a struct tag (a pattern that would be AST-invisible to prong 3b).
//
// In practice, Go struct tags are immutable string constants; runtime
// modification via reflection requires unsafe.Pointer trickery. This test
// asserts the simpler proxy: kernel/cell/ must not import "unsafe".
func TestJWTClaimsNoAuthzEpoch_BlindSpot_DynamicStructTag(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	claimsFile := filepath.Join(root, authzEpochClaimsFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, claimsFile, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "JWT-CLAIMS-NO-AUTHZ-EPOCH-01: parse %s for blind-spot check", authzEpochClaimsFile)

	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		impPath := stripBackticksOrQuotes(imp.Path.Value)
		assert.NotEqualf(t, "unsafe", impPath,
			"JWT-CLAIMS-NO-AUTHZ-EPOCH-01 blind-spot: %s imports \"unsafe\" — "+
				"dynamic struct tag manipulation via unsafe.Pointer would bypass prong 3b. "+
				"Remove the import; struct tags must be static compile-time constants.",
			authzEpochClaimsFile)
	}
}

// TestJWTClaimsNoAuthzEpoch_RedFixtureDetected proves the rule catches a
// regression: the RED fixture has both forms (AuthzEpoch field + literal
// "authz_epoch"), and our detectors flag both.
func TestJWTClaimsNoAuthzEpoch_RedFixtureDetected(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, authzEpochRedFixtureRel)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "JWT-CLAIMS-NO-AUTHZ-EPOCH-01: parse RED fixture %s", authzEpochRedFixtureRel)

	// Detect AuthzEpoch field anywhere in any struct (prong 1 form).
	fieldDetected := false
	scanner.EachInSubtree[ast.StructType](file, func(st *ast.StructType) {
		if st.Fields == nil {
			return
		}
		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				if name != nil && name.Name == authzEpochFieldName {
					fieldDetected = true
				}
			}
		}
	})
	assert.Truef(t, fieldDetected,
		"RED fixture self-check: %s must declare an %s field so prong 1 detects it",
		authzEpochRedFixtureRel, authzEpochFieldName)

	// Detect literal "authz_epoch" anywhere (prongs 2+3 form).
	literalDetected := false
	scanner.EachInSubtree[ast.BasicLit](file, func(bl *ast.BasicLit) {
		if bl.Kind == gotoken.STRING && stripBackticksOrQuotes(bl.Value) == authzEpochClaimKey {
			literalDetected = true
		}
	})
	assert.Truef(t, literalDetected,
		"RED fixture self-check: %s must contain literal %q so prongs 2+3 detect it",
		authzEpochRedFixtureRel, authzEpochClaimKey)
}

// ─── helpers ────────────────────────────────────────────────────────────

// findStructTypeForTypeDecl returns the *ast.StructType associated with the
// first top-level `type name struct {...}` declaration in file. Uses
// scanner.EachInSubtree per SCANNER-FRAMEWORK-USAGE-01.
func findStructTypeForTypeDecl(file *ast.File, name string) *ast.StructType {
	var out *ast.StructType
	scanner.EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if out != nil || ts.Name == nil || ts.Name.Name != name {
			return
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			out = st
		}
	})
	return out
}

// findCompositeLitForVar returns the *ast.CompositeLit on the RHS of the
// first top-level `var name = <CompositeLit>` declaration in file. Uses
// scanner.EachInSubtree per SCANNER-FRAMEWORK-USAGE-01.
func findCompositeLitForVar(file *ast.File, name string) *ast.CompositeLit {
	var out *ast.CompositeLit
	scanner.EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
		if out != nil {
			return
		}
		for i, ident := range vs.Names {
			if ident == nil || ident.Name != name {
				continue
			}
			if i >= len(vs.Values) {
				continue
			}
			if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
				out = cl
				return
			}
		}
	})
	return out
}

// stripBackticksOrQuotes strips a leading and trailing " or ` from s.
func stripBackticksOrQuotes(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '`' && last == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// nodeContains reports whether outer's source range encloses inner.
func nodeContains(outer, inner ast.Node) bool {
	return outer != nil && inner != nil &&
		outer.Pos() <= inner.Pos() && inner.End() <= outer.End()
}
