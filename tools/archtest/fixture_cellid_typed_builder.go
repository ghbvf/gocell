// Importable rule bodies for FIXTURE-CELLID-TYPED-BUILDER-01 and
// METADATATEST-IMPORT-SCOPE-01. Migrated from the legacy _test.go form to a
// non-test .go (M3 #1639) so both rules are module-path-agnostic — platform
// symbol paths (kernel/metadata, kernel/metadata/metadatatest, the carve-out
// map key) are derived from [PlatformModulePath] (external.go), NOT bare
// "github.com/ghbvf/gocell…" string literals (ARCHTEST-MODULE-PATH-FUNNEL-01).
// The self-checks (A2 body-shape, A3 negative fixture, A4 ADR-consistency, A5
// var-initializer shape) live in fixture_cellid_typed_builder_test.go and call
// the helpers defined here — single source, no parallel rule body.
//
// Not registered in StandardCellRules: both invariants are GoCell-internal,
// tied to the kernel/metadata + kernel/metadata/metadatatest layout that no
// external Cell repo replicates. FIXTURE-CELLID-TYPED-BUILDER-01 scans
// kernel/-rooted fixtures for bare cell-id literals; an external repo lacks
// that layout → vacuous-green. METADATATEST-IMPORT-SCOPE-01 guards against
// importing the GoCell-internal metadatatest package from production code; an
// external repo that does not import metadatatest at all is trivially clean.
// Kept importable + module-path-agnostic but OUT of StandardCellRules (same
// disposition as the #1632 auth funnels and CAPABILITY-PROVIDER-FUNNEL-01).
//
// # FIXTURE-CELLID-TYPED-BUILDER-01
//
// Every cell-id field position in a kernel/metadata.* struct or map composite
// literal — anywhere in the module's hand-written code (production + tests, all
// tag combinations) — must be sourced from one of three sanctioned forms:
//
//  1. kernel/metadata/metadatatest.NewCellID(literal) — typed-builder call.
//  2. One of metadatatest's pre-validated package-level cell-id vars (CellID*).
//  3. metadata.FrameworkOwnerSentinel const (#1939) — the reserved "_framework"
//     owner/provider value. It cannot go through NewCellID (leading underscore is
//     not a legal cell id, so NewCellID would panic) and cannot be a CellID* var
//     (A5 forbids non-NewCellID initialisers). It is therefore accepted directly via
//     TypesInfo identity lock (metadataPkgPath + const name). Accepting it at all
//     cell-id positions is harmless: "_framework" is not a legal cell id per
//     MatchCellID, so inadvertent use at CellMeta.ID is caught by FMT-C1. Bare string literals at
//
// those positions are rejected.
//
// Scope: currently kernel/ only. Test fixtures in runtime/, cells/, cmd/,
// examples/, and tools/ outside the migrated tools/codegen +
// tools/generatedverify subset may still embed bare cell-id literals (Soft
// state). Mirror backlog issue #1201 tracks scope expansion to non-kernel/
// packages.
//
// Enforcement is split into sub-rules A1–A5; A1 is the importable rule body
// supplied by [CheckFixtureCellIDTypedBuilder]. A2, A3, A4, A5 are self-checks
// that stay in the _test.go companion and call helpers defined here.
//
// AI-robust: downstream Hard (A1 typed-info callsite identity), upstream Hard
// (A2 body form-uniqueness + pkg-path identity lock), meta Hard (A3 negative
// fixture + A4 ADR consistency). See the _test.go package godoc for the full
// evaluation.
//
// # METADATATEST-IMPORT-SCOPE-01
//
// The metadatatest package (kernel/metadata/metadatatest) must only be imported
// by *_test.go files or archtest_fixture-tagged code. Importing it from
// production code drags init-time panics into runtime and contradicts its
// test-only purpose. [CheckMetadatatestImportScope] is the importable rule body.
//
// AI-robust: Medium, single-axis (path-based archtest scope — NOT a funnel, so
// no caller-allowlist / sealed-construction double-lock applies). Go cannot
// express a "test-only package" at the type level, so the sole Hard-upgrade
// path is a Go language feature that does not exist — a permanent Go-ceiling
// won't-do (no GoCell-side issue; same ceiling family as #851 / #893 / #1282).
// The AST import-scan (production .go importing metadatatest) is the canonical
// enforcement; dot-import is resolved via importsMetadatatest.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	fixtureCellIDRuleID           = "FIXTURE-CELLID-TYPED-BUILDER-01"
	metadatatestImportScopeRuleID = "METADATATEST-IMPORT-SCOPE-01"
	metadatatestNewCellIDFunc     = "NewCellID"
	metadatatestCellIDSourceFile  = "kernel/metadata/metadatatest/cellid.go"
	// fixtureCellIDADRFile is the relative path to the ADR backing A4
	// (carveout map ↔ ADR registry consistency). Rename or move of the ADR
	// file requires synchronized update of this constant in the SAME PR —
	// A4 fails fast with "read ADR" if the file is not found, which is the
	// intended diagnostic.
	fixtureCellIDADRFile = "docs/architecture/202605271000-adr-fixture-cellid-typed-builder.md"

	// metadataPkgPath / metadatatestPkgPath derive from PlatformModulePath so
	// a module rename / /v2 bump updates one place
	// (ARCHTEST-MODULE-PATH-FUNNEL-01).
	metadataPkgPath     = PlatformFrameworkModulePath + "/kernel/metadata"
	metadatatestPkgPath = PlatformFrameworkModulePath + "/kernel/metadata/metadatatest"

	// frameworkOwnerSentinelName is the exact exported const name in
	// kernel/metadata whose package-path is metadataPkgPath. Used by
	// isSanctionedCellIDExpr to recognize FrameworkOwnerSentinel as a
	// sanctioned cell-id source without relying on name-only matching
	// (which would accept a homonymous const from another package).
	frameworkOwnerSentinelName = "FrameworkOwnerSentinel"
)

// cellIDFieldPosition identifies a struct field (or slice-field element
// position) whose string value semantics is a cell-id and therefore must
// be sourced from metadatatest. The 15-field enumeration (14 struct fields
// + 1 map key via cellIDMapKeyValueStructs) mirrors the in-scope table in
// plan #681 / ADR §1; new cell-id fields require a same-PR update here
// AND in the ADR §1 table.
type cellIDFieldPosition struct {
	structName     string // e.g. "CellMeta"; package path is always metadataPkgPath
	fieldName      string // e.g. "ID"
	isSliceElement bool   // true when value is []string and each element is a cell-id
}

var cellIDFieldPositions = []cellIDFieldPosition{
	{"CellMeta", "ID", false},
	{"SliceMeta", "BelongsToCell", false},
	{"L0DepMeta", "Cell", false},
	{"ContractMeta", "OwnerCell", false},
	{"EndpointsMeta", "Server", false},
	{"EndpointsMeta", "Clients", true},
	{"EndpointsMeta", "Publisher", false},
	{"EndpointsMeta", "Handler", false},
	{"EndpointsMeta", "Invokers", true},
	{"EndpointsMeta", "Provider", false},
	{"EndpointsMeta", "Readers", true},
	{"JourneyMeta", "Cells", true},
	// AssemblyMeta.Cells is []AssemblyCellRef (#1086): the cell-id moved from
	// the slice element to AssemblyCellRef.ID, so the enforced position is the
	// ID field of each AssemblyCellRef struct literal (not the slice element).
	{"AssemblyCellRef", "ID", false},
	// CellWireSummary.CellID: the derived wire-catalog struct in derived.go whose
	// CellID field carries cell-id semantics. Fixtures constructing CellWireSummary
	// live in runtime/ (outside A1's current kernel/ scope) and will be enforced
	// once #1201 expands scope; this entry is forward-compatible.
	{"CellWireSummary", "CellID", false},
}

// cellIDMapKeyValueStructs lists the named struct types T such that any
// map[string]*T or map[string]T composite literal has cell-id-semantics
// keys. Only ProjectMeta.Cells qualifies — Slices/Contracts/Journeys/
// Assemblies have composite ID semantics (slice-id paths, contract-id,
// etc.) and are tracked under separate mirror issues.
var cellIDMapKeyValueStructs = map[string]struct{}{
	"CellMeta": {},
}

// fixtureCellIDCarveOuts lists function-qualified names that opt out of
// A1 enforcement at the function-body level. Each entry must mirror an
// ADR §2 row. See A4 TestFixtureCellIDTypedBuilder_CarveOutADRConsistency.
//
// Keys are MODULE-RELATIVE (no platform module-path prefix), so the registry
// carries no "github.com/ghbvf/gocell" literal AND a module rename / /v2 bump
// touches neither this map nor the backing ADR §2 table — A4's
// character-identical compare stays a genuine single-place edit
// (ARCHTEST-MODULE-PATH-FUNNEL-01 + codex #1708 F5). carvedOutFunctions strips
// the platform prefix from each resolved package path before matching.
var fixtureCellIDCarveOuts = map[string]struct{}{
	"framework/kernel/governance.TestValidator_FMTC1_CellIDPattern": {},
}

// CheckFixtureCellIDTypedBuilder enforces FIXTURE-CELLID-TYPED-BUILDER-01
// (A1): every kernel/metadata-typed cell-id field position must be sourced
// from metadatatest. It is the importable rule body — GoCell's
// TestFixtureCellIDTypedBuilder calls it via dogfood, single source.
func CheckFixtureCellIDTypedBuilder(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	allowSelfFile := map[string]struct{}{
		"kernel/metadata/metadatatest/cellid.go":      {},
		"kernel/metadata/metadatatest/cellid_test.go": {},
		// derived.go is the single production construction site for CellWireSummary.
		// cellWireSummaryFrom sets CellID from a function parameter (not a literal),
		// which is semantically correct — A1 does not gate production assignment from
		// runtime values, only fixture literal embedding. CellWireSummary.CellID is in
		// cellIDFieldPositions for forward-compatible enforcement in runtime/ test
		// fixtures (tracked by #1201), not to gate this production constructor.
		"kernel/metadata/derived.go": {},
		// types.go holds metadata.CellRefs — the single sanctioned constructor that
		// embeds AssemblyCellRef{ID: <runtime param>} (cell development in an
		// independent repository, #1086). Same rationale as derived.go: production
		// construction from a runtime value, not a fixture literal.
		"kernel/metadata/types.go": {},
	}

	return Canonical(scanCellIDFixtureViolations(t, allowSelfFile, fixtureCellIDCarveOuts))
}

// CheckMetadatatestImportScope enforces METADATATEST-IMPORT-SCOPE-01: no
// production (.go non-_test.go) file may import metadatatest. It is the
// importable rule body — GoCell's TestMetadatatestImportScope calls it via
// dogfood, single source.
func CheckMetadatatestImportScope(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)
	scope := ModuleScope(root)
	var diags []Diagnostic
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			diags = append(diags, scanMetadatatestImport(p.Rel(file), file, p.Fset)...)
		}
		return nil
	})
	return Canonical(diags)
}

// scanMetadatatestImport returns a structured Diagnostic (anchored at rel:line,
// NOT a stringly ":0:" message — codex #1708 F2) when the production file at rel
// imports the metadatatest package. _test.go files and metadatatest's own package
// are exempt. Pure (no *Pass) so the structured location is unit-testable
// (TestMetadatatestImportScope_StructuredLocation), mirroring
// scanSrcForViolations in seed_role_iface.
func scanMetadatatestImport(rel string, file *ast.File, fset *token.FileSet) []Diagnostic {
	if strings.HasSuffix(rel, "_test.go") {
		return nil
	}
	if strings.HasPrefix(rel, "kernel/metadata/metadatatest/") {
		return nil
	}
	if !importsMetadatatest(file) {
		return nil
	}
	line := fset.Position(file.Pos()).Line
	return []Diagnostic{diagAt(rel, line,
		fmt.Sprintf("production file imports %s — restricted to *_test.go", metadatatestPkgPath))}
}

// scanCellIDFixtureViolations runs Run(t, Typed(...)) over the kernel/ package
// tree (tests=true) twice — once with FlatNonDefaultTags, once with no
// tags — to cover //go:build !X reverse directives. Returns one structured
// Diagnostic per violating position, anchored at Rel:Line (the column is carried
// in Message); duplicates from the two-pass overlap are folded by the caller's
// Canonical.
//
// Scope rationale: this rule's scope tracks the plan and ADR §1 scope
// — kernel/ is where the cell-id metadata fixtures originate and the
// in-scope cell-id field positions live. Non-kernel packages (runtime/,
// cells/, cmd/, examples/, tools/) consume these structs in their own
// fixtures and will be migrated via mirror backlog issues; once each
// such package is migrated, its path prefix is added to the scan scope
// list below and its allowlist entry (if any) removed.
func scanCellIDFixtureViolations(t *testing.T, allowSelfFiles, carveOuts map[string]struct{}) []Diagnostic { //nolint:gocognit,lll // archtest AST scanner: per-file carve-out + composite-literal walk over kernel/ tags×2; complexity inherent to FIXTURE-CELLID-TYPED-BUILDER-01 A1's exhaustive composite-literal walk
	t.Helper()
	scopePrefixes := []string{"kernel/"}
	var violations []Diagnostic
	collect := func(opts TypedOpts) {
		_ = Run(t, Typed(opts, []string{"./framework/kernel/..."}), func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if !hasAnyPrefix(rel, scopePrefixes) {
					continue
				}
				if _, ok := allowSelfFiles[rel]; ok {
					continue
				}
				if p.IsGenerated(file) {
					continue
				}
				carved := carvedOutFunctions(file, p, carveOuts)
				EachInSubtree[ast.CompositeLit](file, func(comp *ast.CompositeLit) {
					if isInsideCarvedFunc(comp, carved) {
						return
					}
					violations = append(violations,
						scanCellIDComposite(p, file, rel, comp)...)
				})
			}
			return nil
		})
	}
	collect(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()})
	collect(TypedOpts{Tests: true})
	return violations
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// carvedOutFunctions returns the set of *ast.FuncDecl ranges within file
// whose qualified name is in carveOuts. The result is a position range
// used by isInsideCarvedFunc.
func carvedOutFunctions(file *ast.File, p *Pass, carveOuts map[string]struct{}) []funcRange {
	if p.Pkg == nil {
		return nil
	}
	// Carve-out keys are module-relative (codex #1708 F5): strip the platform
	// module prefix from the resolved package path before matching. A non-platform
	// package path is left unchanged and matches no GoCell-internal carve-out
	// (correct — every carve-out names a GoCell-internal function).
	relPkg := strings.TrimPrefix(p.Pkg.Path(), PlatformModulePath+"/")
	var out []funcRange
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil {
			return
		}
		qual := relPkg + "." + fd.Name.Name
		if _, ok := carveOuts[qual]; !ok {
			return
		}
		out = append(out, funcRange{start: fd.Pos(), end: fd.End()})
	})
	return out
}

type funcRange struct {
	start, end token.Pos
}

func isInsideCarvedFunc(n ast.Node, carved []funcRange) bool {
	if n == nil || len(carved) == 0 {
		return false
	}
	pos := n.Pos()
	for _, r := range carved {
		if pos >= r.start && pos < r.end {
			return true
		}
	}
	return false
}

// scanCellIDComposite inspects a single CompositeLit and emits one
// structured Diagnostic per cell-id position whose expression is not a
// sanctioned metadatatest reference.
func scanCellIDComposite(p *Pass, _ *ast.File, rel string, comp *ast.CompositeLit) []Diagnostic {
	t := p.TypesInfo.TypeOf(comp)
	if t == nil {
		return nil
	}
	switch under := t.Underlying().(type) {
	case *types.Map:
		return scanCellIDMapComposite(p, rel, comp, under)
	case *types.Struct:
		return scanCellIDStructComposite(p, rel, comp, t)
	case *types.Pointer:
		// Pointer-elided composite literal: []*metadata.ContractMeta{{OwnerCell: "bare"}}
		// has the inner literal typed as *metadata.ContractMeta. t.Underlying() is *types.Pointer
		// (a pointer is its own underlying type). Unwrap to the element type and proceed as
		// if the literal were a struct composite.
		if _, ok := under.Elem().Underlying().(*types.Struct); ok {
			return scanCellIDStructComposite(p, rel, comp, under.Elem())
		}
	}
	return nil
}

func scanCellIDMapComposite(p *Pass, rel string, comp *ast.CompositeLit, m *types.Map) []Diagnostic {
	// Match map[string]*metadata.<Struct> or map[string]metadata.<Struct>
	// where struct name is in cellIDMapKeyValueStructs.
	keyT, ok := m.Key().(*types.Basic)
	if !ok || keyT.Kind() != types.String {
		return nil
	}
	valStruct := namedStructFromType(m.Elem())
	if valStruct == nil {
		return nil
	}
	if valStruct.Obj().Pkg() == nil || valStruct.Obj().Pkg().Path() != metadataPkgPath {
		return nil
	}
	if _, ok := cellIDMapKeyValueStructs[valStruct.Obj().Name()]; !ok {
		return nil
	}
	var out []Diagnostic
	valName := valStruct.Obj().Name()
	EachInChildren[ast.KeyValueExpr](comp, func(kv *ast.KeyValueExpr) {
		if !isSanctionedCellIDExpr(p, kv.Key) {
			out = append(out, fmtPositionViolation(p, rel, kv.Key, "map[string]*metadata."+valName+" key"))
		}
	})
	return out
}

func scanCellIDStructComposite(p *Pass, rel string, comp *ast.CompositeLit, t types.Type) []Diagnostic { //nolint:gocognit,lll // archtest AST scanner: keyed/positional struct-literal field-position resolution; complexity inherent to FIXTURE-CELLID-TYPED-BUILDER-01's keyed/positional struct handling
	named, ok := t.(*types.Named)
	if !ok {
		// pointer to named?
		if ptr, isPtr := t.(*types.Pointer); isPtr {
			named, ok = ptr.Elem().(*types.Named)
			if !ok {
				return nil
			}
		} else {
			return nil
		}
	}
	if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != metadataPkgPath {
		return nil
	}
	structName := named.Obj().Name()
	var out []Diagnostic
	EachInChildren[ast.KeyValueExpr](comp, func(kv *ast.KeyValueExpr) {
		keyIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			return
		}
		fieldName := keyIdent.Name
		pos, found := lookupCellIDFieldPosition(structName, fieldName)
		if !found {
			return
		}
		if pos.isSliceElement {
			// value is *ast.CompositeLit []string{...} or []string{} ref
			sliceComp, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				// not a literal slice — could be Ident to a known slice; skip
				// (rare in fixtures; archtest A1 focuses on inline composites)
				return
			}
			for _, sliceElt := range sliceComp.Elts {
				if !isSanctionedCellIDExpr(p, sliceElt) {
					out = append(out, fmtPositionViolation(p, rel, sliceElt, "metadata."+structName+"."+fieldName+"[i]"))
				}
			}
		} else if !isSanctionedCellIDExpr(p, kv.Value) {
			out = append(out, fmtPositionViolation(p, rel, kv.Value, "metadata."+structName+"."+fieldName))
		}
	})
	return out
}

func namedStructFromType(t types.Type) *types.Named {
	if t == nil {
		return nil
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return nil
	}
	return named
}

func lookupCellIDFieldPosition(structName, fieldName string) (cellIDFieldPosition, bool) {
	for _, p := range cellIDFieldPositions {
		if p.structName == structName && p.fieldName == fieldName {
			return p, true
		}
	}
	return cellIDFieldPosition{}, false
}

// isSanctionedCellIDExpr reports whether expr is a sanctioned cell-id
// source. Three forms are accepted:
//
//  1. metadatatest.NewCellID(BasicLit STRING) CallExpr — the primary typed-builder
//     path; callee identity is locked via TypesInfo to metadatatestPkgPath.
//
//  2. SelectorExpr resolving to a metadatatest package-level Var whose name has
//     the "CellID" prefix — the closed enumeration path (CellIDAccessCore, etc.).
//     A5 guarantees that every such var was built via NewCellID(literal).
//
//  3. metadata.FrameworkOwnerSentinel const (#1939) — the reserved "_framework"
//     sentinel is a first-class legal value at owner/provider cell-id positions.
//     NewCellID("_framework") panics (leading underscore is not a legal cell id),
//     and metadatatest cannot add a CellID* alias for it (A5 would reject the
//     non-NewCellID initializer). The sentinel is therefore recognized directly via
//     TypesInfo identity lock: package path == metadataPkgPath AND object kind ==
//     *types.Const AND name == "FrameworkOwnerSentinel". This is precise — a
//     homonymous const in another package resolves to a different package path and
//     is rejected. Accepting sentinel at all cell-id positions (not just OwnerCell)
//     is harmless: "_framework" is not a legal cell id per MatchCellID, so if it
//     inadvertently appears at e.g. CellMeta.ID the existing FMT-C1 rule will
//     catch it; A1's job is to reject bare string literals, not to enforce
//     field-position semantics beyond what field-position enumeration already
//     expresses. Cross-reference: ADR 202606130635-1939-adr-framework-owned-contract.md.
//
// Any other shape — bare BasicLit, Ident→BasicLit chain, dynamic NewCellID arg,
// third-party const ref — is rejected.
func isSanctionedCellIDExpr(p *Pass, expr ast.Expr) bool { //nolint:gocognit,cyclop,lll // archtest AST scanner: enumerates sanctioned NewCellID/typed-var/sentinel expr forms (selector/ident/call/const); complexity inherent to FIXTURE-CELLID-TYPED-BUILDER-01's sanctioned-form enumeration
	switch e := expr.(type) {
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn == nil || fn.Pkg() == nil {
			// Try package-level func via Uses (non-method call).
			ident := sel.Sel
			obj := p.TypesInfo.Uses[ident]
			f, isFn := obj.(*types.Func)
			if !isFn || f.Pkg() == nil {
				return false
			}
			// Reject methods (receiver != nil): a method named NewCellID on some
			// other type could share the same pkg+name and slip through if we only
			// checked package path and function name.
			if f.Signature().Recv() != nil {
				return false
			}
			if f.Pkg().Path() != metadatatestPkgPath || f.Name() != metadatatestNewCellIDFunc {
				return false
			}
		} else if fn.Pkg().Path() != metadatatestPkgPath || fn.Name() != metadatatestNewCellIDFunc {
			return false
		}
		if len(e.Args) != 1 {
			return false
		}
		lit, ok := e.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return false
		}
		return true
	case *ast.SelectorExpr:
		obj := p.TypesInfo.Uses[e.Sel]
		// Form 3: metadata.FrameworkOwnerSentinel const — identity-locked via
		// TypesInfo to metadataPkgPath + exact name; package-path lock is Hard.
		if c, isConst := obj.(*types.Const); isConst {
			return c.Pkg() != nil &&
				c.Pkg().Path() == metadataPkgPath &&
				c.Name() == frameworkOwnerSentinelName
		}
		// Form 2: metadatatest.<CellIDVar> — the var name must have a "CellID"
		// prefix so that future non-CellID vars added to the metadatatest package
		// are not silently accepted as sanctioned cell-id sources.
		v, ok := obj.(*types.Var)
		if !ok || v.Pkg() == nil {
			return false
		}
		if v.Pkg().Path() != metadatatestPkgPath {
			return false
		}
		return strings.HasPrefix(v.Name(), "CellID")
	case *ast.Ident:
		// Form 3 (in-package usage): bare FrameworkOwnerSentinel inside package
		// metadata itself — same identity lock as the SelectorExpr branch.
		obj := p.TypesInfo.Uses[e]
		c, isConst := obj.(*types.Const)
		if !isConst || c.Pkg() == nil {
			return false
		}
		return c.Pkg().Path() == metadataPkgPath && c.Name() == frameworkOwnerSentinelName
	}
	return false
}

// fmtPositionViolation builds a structured Diagnostic anchored at rel:line
// (codex #1708 F2 — not a stringly ":0:" Diagnostic{Message}). The column is
// carried in Message since Diagnostic has no column field.
func fmtPositionViolation(p *Pass, rel string, expr ast.Expr, fieldPath string) Diagnostic {
	pos := p.Fset.Position(expr.Pos())
	return diagAt(rel, pos.Line,
		fmt.Sprintf("col %d: %s — expected metadatatest.NewCellID(literal) or metadatatest.<CellIDVar>, got %s",
			pos.Column, fieldPath, exprSourceSnippet(expr)))
}

func exprSourceSnippet(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value
	case *ast.Ident:
		return "ident " + e.Name
	case *ast.SelectorExpr:
		// Return <pkg>.<Name> to give actionable context in violation messages.
		if xIdent, ok := e.X.(*ast.Ident); ok {
			return xIdent.Name + "." + e.Sel.Name
		}
		return "selector." + e.Sel.Name
	case *ast.CallExpr:
		// Return <funcName>() for actionable context.
		switch fn := e.Fun.(type) {
		case *ast.Ident:
			return fn.Name + "()"
		case *ast.SelectorExpr:
			if xIdent, ok := fn.X.(*ast.Ident); ok {
				return xIdent.Name + "." + fn.Sel.Name + "()"
			}
			return fn.Sel.Name + "()"
		}
		return "call()"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

// isMatchCellIDNegateCond reports whether cond is the negated MatchCellID
// call expression `!metadata.MatchCellID(s)`, with the callee resolved via
// TypesInfo to the expected package path. Used by A2 body-shape check.
func isMatchCellIDNegateCond(pInfo *types.Info, cond ast.Expr) bool {
	unary, ok := cond.(*ast.UnaryExpr)
	if !ok || unary.Op != token.NOT {
		return false
	}
	call, ok := unary.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	// F1: verify MatchCellID resolves to metadata.MatchCellID via TypesInfo —
	// matching .Sel.Name == "MatchCellID" alone is Soft (any package with
	// same-name function slips through). Package-path lock is Hard.
	if pInfo != nil {
		obj := pInfo.Uses[sel.Sel]
		fn, isFn := obj.(*types.Func)
		if !isFn || fn.Pkg() == nil || fn.Pkg().Path() != metadataPkgPath || fn.Name() != "MatchCellID" {
			return false
		}
	} else if sel.Sel.Name != "MatchCellID" {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	argIdent, ok := call.Args[0].(*ast.Ident)
	if !ok || argIdent.Name != "s" {
		return false
	}
	return true
}

// importsMetadatatest reports whether file has an import path equal to
// metadatatestPkgPath.
func importsMetadatatest(file *ast.File) bool {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if path == metadatatestPkgPath {
			return true
		}
	}
	return false
}

// parseCarveOutTableFromADR extracts function qualified names from the
// ADR's §2 carveout registry markdown table. The table is recognized by
// a header line starting with "| Carved-out function" and ending at the
// next blank line / non-table line.
func parseCarveOutTableFromADR(content string) (map[string]struct{}, error) { //nolint:gocognit,lll // archtest ADR-table parser: markdown row-state machine for A4 carve-out consistency; complexity inherent to the markdown row-state parse
	out := map[string]struct{}{}
	lines := strings.Split(content, "\n")
	inTable := false
	headerRe := regexp.MustCompile(`^\|\s*Carved-out function\s*\|`)
	dividerRe := regexp.MustCompile(`^\|[\s\-:]+\|`)
	rowRe := regexp.MustCompile(`^\|\s*([^|]+?)\s*\|`)
	for _, line := range lines {
		if !inTable {
			if headerRe.MatchString(line) {
				inTable = true
			}
			continue
		}
		// Skip the divider row after header.
		if dividerRe.MatchString(line) {
			continue
		}
		// End of table — empty line or non-pipe line.
		if !strings.HasPrefix(line, "|") {
			break
		}
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		fqn := strings.TrimSpace(m[1])
		if fqn == "" || strings.HasPrefix(fqn, "-") {
			continue
		}
		out[fqn] = struct{}{}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no carveout rows found under '| Carved-out function |' header")
	}
	return out, nil
}
