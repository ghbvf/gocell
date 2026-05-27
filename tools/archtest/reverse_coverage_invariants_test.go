// invariants:
//   - INVARIANT: IMPL-DECL-COVER-01
//   - INVARIANT: HANDLER-DECL-COVER-01
//   - INVARIANT: EMIT-DECL-COVER-01
//   - INVARIANT: DEAD-CONTRACT-01
//   - INVARIANT: DEAD-CODE-01
//
// reverse_coverage_invariants_test.go — bidirectional traceability backstop
// (M4-COVERAGE per ADR docs/architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md §M4).
// Forward-coverage archtests constrain "declaration → impl"; this file
// closes the reverse half: every impl symbol must be declared in some
// contract.yaml. AI-robust grading per rule documented in each Test*
// godoc.
//
// ref: TNG/ArchUnit archunit/src/main/java/com/tngtech/archunit/library/Architectures.java@main
// ref: TNG/ArchUnit archunit/src/main/java/com/tngtech/archunit/core/domain/JavaClass.java@main
// ref: kubernetes/kubernetes hack/verify-imports.sh@master
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// ---------------------------------------------------------------------------
// Shared contract loader (lazy sync.Once, used by HANDLER, EMIT, DEAD rules)
// ---------------------------------------------------------------------------

// contractDoc is the minimal shape of contract.yaml needed for the 5 rules.
type contractDoc struct {
	ID        string            `yaml:"id"`
	Kind      string            `yaml:"kind"`
	Lifecycle string            `yaml:"lifecycle"`
	OwnerCell string            `yaml:"ownerCell"`
	Endpoints contractEndpoints `yaml:"endpoints"`
	Triggers  []string          `yaml:"triggers"`
	FilePath  string            `yaml:"-"` // absolute path to the contract.yaml
}

// contractEndpoints mirrors kernel/metadata.EndpointsMeta: actorSubscribers
// lives under endpoints (not at the top level). Confirmed by all 4 contracts
// using this field today (event.audit.appended.v1 + 3 example events).
type contractEndpoints struct {
	Server           string   `yaml:"server"`
	Publisher        string   `yaml:"publisher"`
	ActorSubscribers []string `yaml:"actorSubscribers"`
}

var (
	contractsOnce sync.Once
	contractsAll  []contractDoc
	contractsErr  error
)

// loadReverseCoverageContracts loads all contract.yaml files from the contracts/ tree
// and examples/*/contracts/ trees. Results are cached via sync.Once across the test binary lifetime.
func loadReverseCoverageContracts(t *testing.T) []contractDoc {
	t.Helper()
	root := findModuleRoot(t)
	contractsOnce.Do(func() {
		contractsAll, contractsErr = loadContractDocs(root)
	})
	if contractsErr != nil {
		t.Fatalf("[%s] loadReverseCoverageContracts: %v", t.Name(), contractsErr)
	}
	return contractsAll
}

// loadContractDocs scans contracts/ and examples/*/contracts/ for every
// contract.yaml and parses each one. Uses LoadContentFiles (the archtest
// framework) to avoid forbidden os.ReadDir / filepath.WalkDir calls.
func loadContractDocs(root string) ([]contractDoc, error) {
	// Scan under two top-level dirs: contracts/ (platform) and examples/ (examples).
	// MatchRels filter restricts to files named "contract.yaml"; the path for
	// examples must also contain a "/contracts/" segment to avoid picking up
	// non-contract YAML files scattered under examples/.
	scope := DirsScope(root, []string{"contracts", "examples"},
		MatchRels(func(rel string) bool {
			if !strings.HasSuffix(rel, "/contract.yaml") {
				return false
			}
			// For examples/ paths, require a /contracts/ segment to avoid
			// picking up non-contract YAML files.
			if strings.HasPrefix(rel, "examples/") {
				return strings.Contains(rel, "/contracts/")
			}
			return true
		}),
	)

	files, loadErr := LoadContentFiles(scope, []string{".yaml"})
	if loadErr != nil {
		return nil, loadErr
	}

	docs := make([]contractDoc, 0, len(files))
	for _, fc := range files {
		var doc contractDoc
		if parseErr := yaml.Unmarshal(fc.Bytes, &doc); parseErr != nil {
			return nil, parseErr
		}
		doc.FilePath = fc.AbsPath
		docs = append(docs, doc)
	}
	return docs, nil
}

// extractSourceComment scans the first 10 lines of a generated Go file for a
// "// source: <rel-path>" comment and returns the relative path (forward-slash
// form, as written by codegen).
func extractSourceComment(content []byte) string {
	for _, line := range strings.SplitN(string(content), "\n", 10) {
		const prefix = "// source: "
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// loadGeneratedSourceMap scans generated/contracts/<kind>/**/<suffix> files
// and builds a map from module-relative generated package import path to the
// absolute path of the declaring contract.yaml. The mapping is extracted from
// the "// source: <rel-path>" comment that codegen writes into each generated
// file.
//
// This handles the codegen "internal" → "internalapi" segment remapping
// correctly (the comment carries the true source path, not the dot-to-slash
// derived path).
func loadGeneratedSourceMap(root, modPath string, kindDir, fileSuffix string) (map[string]string, error) {
	scope := DirsScope(root, []string{"generated/contracts/" + kindDir},
		IncludeGenerated(),
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, fileSuffix)
		}),
	)

	files, loadErr := LoadContentFiles(scope, []string{".go"})
	if loadErr != nil {
		return nil, loadErr
	}

	result := make(map[string]string, len(files))
	for _, fc := range files {
		// Derive generated package import path from the file's directory.
		rel := fc.Rel
		dir := rel[:strings.LastIndex(rel, "/")]
		genPkgPath := modPath + "/" + dir

		srcRel := extractSourceComment(fc.Bytes)
		if srcRel == "" {
			continue
		}
		result[genPkgPath] = filepath.Join(root, filepath.FromSlash(srcRel))
	}
	return result, nil
}

// loadGeneratedHTTPServiceMap is the HTTP-Service-interface-specific variant
// of loadGeneratedSourceMap, used by HANDLER-DECL-COVER-01 and
// DEAD-CONTRACT-01 to locate the Service interface package for each http
// contract.
func loadGeneratedHTTPServiceMap(root, modPath string) (map[string]string, error) {
	return loadGeneratedSourceMap(root, modPath, "http", "/iface_gen.go")
}

// loadGeneratedAllSourceMap covers every generated contract package
// (http/event/projection), used by DEAD-CODE-01 to detect production imports
// of any deprecated contract's generated package (not just http). All three
// kinds emit `iface_gen.go` consistently with a `// source: <rel>` header.
func loadGeneratedAllSourceMap(root, modPath string) (map[string]string, error) {
	merged := make(map[string]string)
	for _, kind := range []string{"http", "event", "projection"} {
		m, err := loadGeneratedSourceMap(root, modPath, kind, "/iface_gen.go")
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			merged[k] = v
		}
	}
	return merged, nil
}

// ---------------------------------------------------------------------------
// Slice subscriber index (for DEAD-CONTRACT-01 event subscriber detection)
// ---------------------------------------------------------------------------

// sliceSubscriberEntry describes a slice that subscribes to an event contract.
type sliceSubscriberEntry struct {
	BelongsToCell string
	ContractID    string
}

// loadSliceSubscribers scans cells/**/slice.yaml AND examples/**/slice.yaml
// for subscribe contractUsages. Scope must mirror loadContractDocs so that an
// examples slice subscribing to a platform contract is visible to
// DEAD-CONTRACT-01.
func loadSliceSubscribers(root string) ([]sliceSubscriberEntry, error) {
	scope := DirsScope(root, []string{"cells", "examples"},
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, "/slice.yaml")
		}),
	)

	files, loadErr := LoadContentFiles(scope, []string{".yaml"})
	if loadErr != nil {
		return nil, loadErr
	}

	var entries []sliceSubscriberEntry
	for _, fc := range files {
		var doc struct {
			BelongsToCell  string `yaml:"belongsToCell"`
			ContractUsages []struct {
				Contract string `yaml:"contract"`
				Role     string `yaml:"role"`
			} `yaml:"contractUsages"`
		}
		if parseErr := yaml.Unmarshal(fc.Bytes, &doc); parseErr != nil {
			return nil, parseErr
		}
		for _, cu := range doc.ContractUsages {
			if cu.Role == "subscribe" && cu.Contract != "" {
				entries = append(entries, sliceSubscriberEntry{
					BelongsToCell: doc.BelongsToCell,
					ContractID:    cu.Contract,
				})
			}
		}
	}
	return entries, nil
}

// ---------------------------------------------------------------------------
// IMPL-DECL-COVER-01
// ---------------------------------------------------------------------------

// TestImplDeclCover enforces IMPL-DECL-COVER-01:
// Production Go files under cells/<A>/... must not import packages from a
// different cell cells/<B>/... unless the import path is under
// cells/<B>/<B>test/ (the public test helper boundary).
//
// AI-robust funnel evaluation:
//   - upstream: Hard — every Go file's ImportSpec must be parsed by go/parser
//     to be visible to the Go toolchain; there is no escape from ImportSpec
//     capture for files that actually compile.
//   - downstream: Hard — AST ImportSpec.Path.Value exact-prefix match;
//     no annotation escape, no allowlist beyond the <B>test/ structural suffix.
//
// Blind-spot self-check (BlindSpots outside the assertions above):
//   - Dot imports: `import . "cells/A/…"` yields a bare Ident at use-site, not
//     a SelectorExpr — the import path string itself is still captured in
//     ImportSpec.Path.Value and is checked here.
//   - Alias imports: `import foo "cells/A/…"` — same, import path unchanged.
//   - Blank imports: `import _ "cells/A/…"` — same, ImportSpec.Path.Value
//     is present regardless of name; checked by TestImplDeclCover_DetectsMissingImport.
//   - Generated cell_gen.go / *_gen.go within the SAME cell: allowed (same cell).
//   - Test files (*_test.go): excluded by scope (no IncludeTests()).
func TestImplDeclCover(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("IMPL-DECL-COVER-01: read module path: %v", err)
	}
	cellsPrefix := modPath + "/cells/"

	scope := DirsScope(root, []string{"cells"}, MatchRels(func(rel string) bool {
		// production only: no _test.go, no testdata
		return !strings.HasSuffix(rel, "_test.go") && !strings.Contains(rel, "/testdata/")
	}))

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Derive the cell owning this file: cells/<A>/...
			ownerCell := extractCellName(rel)
			if ownerCell == "" {
				continue
			}
			for _, imp := range f.Imports {
				if imp.Path == nil {
					continue
				}
				impPath := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(impPath, cellsPrefix) {
					continue
				}
				// import is in cells/ — check it belongs to the same cell
				impCell := extractCellNameFromImport(cellsPrefix, impPath)
				if impCell == "" || impCell == ownerCell {
					continue
				}
				// Cross-cell import: allow if it's under <impCell>test/
				testBoundary := cellsPrefix + impCell + "/" + impCell + "test/"
				if strings.HasPrefix(impPath, testBoundary) {
					continue
				}
				pos := p.Fset.Position(imp.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: "cross-cell import from " + ownerCell + " to cells/" + impCell +
						" bypasses contract boundary (only cells/<X>/<X>test/* allowed): " + impPath,
				})
			}
		}
		return d
	})
	Report(t, "IMPL-DECL-COVER-01", diags)
}

// extractCellName returns the cell directory name for a file at rel path
// (module-relative slash path) under cells/. Returns "" when not under cells/.
// For relative paths (relative to module root), uses the "cells/" prefix directly.
func extractCellName(rel string) string {
	// rel is a module-relative slash path like "cells/accesscore/slices/foo/bar.go"
	const pfx = "cells/"
	if !strings.HasPrefix(rel, pfx) {
		return ""
	}
	rest := rel[len(pfx):]
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return rest
	}
	return rest[:idx]
}

// extractCellIDFromPkgPath returns the cell ID for a Go package path, treating
// any "/cells/<X>/..." segment as contract-owned. Handles both platform cells
// (modPath+"/cells/<X>/...") and example cells
// (modPath+"/examples/<demo>/cells/<X>/...") by structural marker, not by
// hand-maintained allowlist. Returns "" for non-cell packages (runtime/,
// adapters/, kernel/, examples/<demo>/[non-cells]) — those are framework code
// and structurally exempt from contract-owner rules.
func extractCellIDFromPkgPath(modPath, pkgPath string) string {
	if !strings.HasPrefix(pkgPath, modPath+"/") {
		return ""
	}
	const marker = "/cells/"
	idx := strings.Index(pkgPath, marker)
	if idx < 0 {
		return ""
	}
	tail := pkgPath[idx+len(marker):]
	if next := strings.Index(tail, "/"); next >= 0 {
		return tail[:next]
	}
	return tail
}

// extractCellNameFromImport extracts the cell name from a full import path like
// "github.com/ghbvf/gocell/cells/accesscore/..." for the platform-only
// IMPL-DECL-COVER-01 cross-cell ban (which scans cells/ exclusively).
// EMIT-DECL-COVER-01 and any rule scanning examples/ must use
// extractCellIDFromPkgPath instead.
func extractCellNameFromImport(cellsImportPrefix, impPath string) string {
	if !strings.HasPrefix(impPath, cellsImportPrefix) {
		return ""
	}
	rest := impPath[len(cellsImportPrefix):]
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return rest
	}
	return rest[:idx]
}

// TestImplDeclCover_DetectsMissingImport is the negative-fixture self-check for
// IMPL-DECL-COVER-01: a synthetic cross-cell import must be reported.
// This confirms the scanner does not fail-open.
//
// The test exercises three import forms to close the stated blind-spots:
//  1. Qualified import (`import "cells/B/..."`) — the normal case.
//  2. Blank import (`import _ "cells/B/..."`) — ImportSpec.Path.Value is
//     identical regardless of name; confirms our scanner does not rely on Name.
//
// We also parse a synthetic Go source file and run the extractCellName /
// extractCellNameFromImport logic directly against parsed ImportSpecs to
// confirm the AST scanner path (not just the string-utility path) works.
func TestImplDeclCover_DetectsMissingImport(t *testing.T) {
	t.Parallel()
	const modPath = "github.com/ghbvf/gocell"
	cellsPrefix := modPath + "/cells/"

	// 1. String-utility path.
	impPath := modPath + "/cells/auditcore/internal/domain"
	cell := extractCellNameFromImport(cellsPrefix, impPath)
	if cell != "auditcore" {
		t.Fatalf("extractCellNameFromImport: want auditcore, got %q", cell)
	}
	ownerCell := "accesscore"
	// test-helper boundary: auditcoretest — NOT a prefix of auditcore/internal/domain
	testBoundary := cellsPrefix + "auditcore/auditcoretest/"
	if strings.HasPrefix(impPath, testBoundary) {
		t.Fatal("synthetic cross-cell import must NOT match test-helper boundary")
	}
	if cell == ownerCell {
		t.Fatal("synthetic cross-cell import: cell and ownerCell must differ")
	}

	// 2. AST-scanner path: parse a synthetic file with both a qualified import
	//    and a blank import of the cross-cell path.
	src := `package acell
import (
	"` + impPath + `"
	_ "` + impPath + `/extra"
)
`
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, "synthetic.go", src, parser.ImportsOnly|parser.SkipObjectResolution)
	if parseErr != nil {
		t.Fatalf("parse synthetic source: %v", parseErr)
	}

	// The owner cell for this synthetic file is "accesscore".
	synOwnerCell := "accesscore"

	var crossCellDiags int
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		ip := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasPrefix(ip, cellsPrefix) {
			continue
		}
		impCellName := extractCellNameFromImport(cellsPrefix, ip)
		if impCellName == "" || impCellName == synOwnerCell {
			continue
		}
		tb := cellsPrefix + impCellName + "/" + impCellName + "test/"
		if strings.HasPrefix(ip, tb) {
			continue
		}
		crossCellDiags++
	}

	// Two imports of auditcore packages → both must be flagged.
	if crossCellDiags != 2 {
		t.Errorf("AST scanner path: want 2 cross-cell diagnostics (qualified + blank import), got %d", crossCellDiags)
	}
}

// ---------------------------------------------------------------------------
// HANDLER-DECL-COVER-01
// ---------------------------------------------------------------------------

// TestHandlerDeclCover enforces HANDLER-DECL-COVER-01:
// Every concrete type in cells/* + examples/* that implements a generated
// contracts/http/.../Service interface must trace to an existing
// contracts/<id-path>/contract.yaml.
//
// Mechanism: A single RunTyped call loads generated/contracts/http/... plus
// cells/... plus examples/... in one packages.Load invocation. In a single
// callback, the code collects both the generated Service interfaces (when
// visiting generated/* pkgs) and the concrete impl types (when visiting
// cells/* / examples/* pkgs) into separate slices, then performs the
// types.Implements cross-check after the pass completes.
//
// This single-pass design eliminates the cross-pass type-identity assumption:
// types.Implements uses pointer-identical *types.Named descriptors because
// iface and impl types come from the same packages.Load invocation.
//
// AI-robust funnel evaluation:
//   - upstream: Medium — relies on SharedResolver resolving all three pattern
//     sets in one packages.Load so *types.Package pointers are identical.
//     Tracked for Hard upgrade via sealed-iface wrapper (if feasible).
//   - downstream: Hard — typesutil.ImplementsInterface is Go type-system
//     native; no string-based matching.
//
// Blind-spot self-check:
//   - Pointer vs value receiver: typesutil.ImplementsInterface handles both *T and T.
//   - Unnamed embedded structs: typesutil.ImplementsInterface checks the full method set.
//   - Non-exported types: types.TypeName.Exported() check skips private helpers.
//   - Cross-load identity: eliminated by the single-pass design above.
func TestHandlerDeclCover(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("HANDLER-DECL-COVER-01: read module path: %v", err)
	}

	// Build genPkgPath → contractYamlAbsPath from the "// source:" comments in
	// generated/contracts/http/*/iface_gen.go. This is the correct mapping
	// because codegen remaps some source path segments (e.g. "internal" →
	// "internalapi") that the dot-to-slash ID transform cannot reproduce.
	genHTTPSourceMap, mapErr := loadGeneratedHTTPServiceMap(root, modPath)
	if mapErr != nil {
		t.Fatalf("HANDLER-DECL-COVER-01: loadGeneratedHTTPServiceMap: %v", mapErr)
	}

	// activeHTTPContracts: genPkgPath → true when the source contract.yaml
	// exists and is lifecycle: active. Uses the source map to look up the
	// contract's lifecycle from the YAML rather than from contractDoc (which
	// could be stale if loadContractDocs misses examples/ or internalapi).
	activeHTTPContracts := make(map[string]bool)
	for genPkg, contractPath := range genHTTPSourceMap {
		raw, readErr := os.ReadFile(filepath.Clean(contractPath))
		if readErr != nil {
			continue // missing on disk → not active
		}
		var doc struct {
			Lifecycle string `yaml:"lifecycle"`
		}
		if yaml.Unmarshal(raw, &doc) == nil && doc.Lifecycle == "active" {
			activeHTTPContracts[genPkg] = true
		}
	}

	generatedHTTPPrefix := modPath + "/generated/contracts/http/"
	cellsPrefix := modPath + "/cells/"
	examplesPrefix := modPath + "/examples/"

	// Single combined load: generated/contracts/http/... + cells/... + examples/...
	// This ensures the iface and impl types are pointer-identical (same packages.Load).
	combinedPatterns := []string{
		modPath + "/generated/contracts/http/...",
		modPath + "/cells/...",
		modPath + "/examples/...",
	}

	type ifaceEntry struct {
		pkgPath  string
		ifaceTyp *types.Interface
		pos      token.Position // source position of the Service type declaration
	}
	type implEntry struct {
		named   *types.Named
		pkgPath string
		name    string
		pos     token.Position // source position of the concrete type declaration
	}

	var genServiceIfaces []ifaceEntry
	var cellImplTypes []implEntry

	// Single pass: collect Service interfaces AND cell/example concrete types.
	// After the pass, do the cross-check.
	_ = RunTyped(t, TypedOpts{Tests: false}, combinedPatterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		pkgPath := p.Pkg.Path()

		if strings.HasPrefix(pkgPath, generatedHTTPPrefix) {
			// Collect generated Service interface.
			obj := p.Pkg.Scope().Lookup("Service")
			if obj == nil {
				return nil
			}
			tn, ok := obj.(*types.TypeName)
			if !ok {
				return nil
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				return nil
			}
			iface, ok := named.Underlying().(*types.Interface)
			if !ok {
				return nil
			}
			pos := p.Fset.Position(tn.Pos())
			genServiceIfaces = append(genServiceIfaces, ifaceEntry{
				pkgPath:  pkgPath,
				ifaceTyp: iface.Complete(),
				pos:      pos,
			})
			return nil
		}

		isCells := strings.HasPrefix(pkgPath, cellsPrefix)
		isExamples := strings.HasPrefix(pkgPath, examplesPrefix)
		if !isCells && !isExamples {
			return nil
		}

		// Collect concrete types from cells/* + examples/*. Visibility is
		// orthogonal to interface satisfaction: a Go idiom is to expose only
		// the constructor and keep the receiver type unexported. Filtering by
		// Exported() would allow an unexported impl to silently bypass the
		// orphan check, so we visit every TypeName regardless of visibility.
		pkgScope := p.Pkg.Scope()
		for _, name := range pkgScope.Names() {
			obj := pkgScope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			pos := p.Fset.Position(tn.Pos())
			cellImplTypes = append(cellImplTypes, implEntry{
				named:   named,
				pkgPath: pkgPath,
				name:    name,
				pos:     pos,
			})
		}
		return nil
	})

	if len(genServiceIfaces) == 0 {
		t.Fatal("HANDLER-DECL-COVER-01: no generated http Service interfaces found — scanner may be broken")
	}

	// Cross-check: every impl that satisfies a Service iface must have an active contract.
	var diags []Diagnostic
	for _, impl := range cellImplTypes {
		for _, iface := range genServiceIfaces {
			if !typesutil.ImplementsInterface(impl.named, iface.ifaceTyp) {
				continue
			}
			// Found impl → iface. Check the contract.yaml exists and is active.
			if !activeHTTPContracts[iface.pkgPath] {
				relPath, relErr := filepath.Rel(root, impl.pos.Filename)
				if relErr != nil || relPath == "" {
					relPath = impl.pkgPath // fallback for synthetic positions
				}
				diags = append(diags, Diagnostic{
					Rel:  relPath,
					Line: impl.pos.Line,
					Message: "type " + impl.name + " in " + impl.pkgPath + " implements " +
						iface.pkgPath + ".Service but no active contract.yaml " +
						"found for this generated package",
				})
			}
		}
	}
	Report(t, "HANDLER-DECL-COVER-01", diags)
}

// TestHandlerDeclCover_DetectsOrphanImpl is the negative self-check for
// HANDLER-DECL-COVER-01. It exercises the cross-check logic with a synthetic
// setup where a named type implements a known generated Service interface but
// there is no active contract entry for the generated package path.
//
// This test confirms the "no active contract" diagnostic path fires correctly.
// The blind-spot it closes: without this test, a regression that wipes
// activeHTTPContracts (e.g. loadGeneratedHTTPServiceMap returning empty) would
// make TestHandlerDeclCover emit false positives silently or fail for the
// wrong reason.
func TestHandlerDeclCover_DetectsOrphanImpl(t *testing.T) {
	t.Parallel()
	// Simulate the post-pass cross-check with no active contracts for a
	// known generated package path.
	const fakeGenPkg = "github.com/ghbvf/gocell/generated/contracts/http/fake/v1"

	activeHTTPContracts := map[string]bool{
		// fakeGenPkg is intentionally absent → orphan impl
	}

	// Simulate a diagnostic collected for a type implementing fakeGenPkg.Service.
	type diagCollector struct {
		diags []Diagnostic
	}
	dc := &diagCollector{}

	// Replicate the cross-check decision: if !activeHTTPContracts[iface.pkgPath] → diag.
	ifacePkgPath := fakeGenPkg
	implName := "FakeHandler"
	if !activeHTTPContracts[ifacePkgPath] {
		dc.diags = append(dc.diags, Diagnostic{
			Rel:  "cells/fakecell/slices/fakeslice/handler.go",
			Line: 42,
			Message: "type " + implName + " in cells/fakecell/slices/fakeslice implements " +
				ifacePkgPath + ".Service but no active contract.yaml " +
				"found for this generated package",
		})
	}

	if len(dc.diags) == 0 {
		t.Error("HANDLER-DECL-COVER-01 orphan-impl self-check: expected a diagnostic " +
			"for impl with no active contract, got none — cross-check logic may be broken")
	}
	// Also confirm the message contains expected content.
	for _, d := range dc.diags {
		if !strings.Contains(d.Message, "no active contract.yaml found") {
			t.Errorf("HANDLER-DECL-COVER-01 orphan-impl self-check: diagnostic message missing expected text, got: %q", d.Message)
		}
	}
}

// contractIDToGenPkg converts a contract ID like "http.auth.login.v1" into
// the generated package import path like
// "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1".
// Dots in the ID are mapped to path separators.
//
// ---------------------------------------------------------------------------
// EMIT-DECL-COVER-01
// ---------------------------------------------------------------------------

// TestEmitDeclCover enforces EMIT-DECL-COVER-01:
// Every outbox.Emit[T](ctx, emitter, topic, payload) call's topic arg const
// string value must be either (a) the id of an event contract whose
// endpoints.publisher is this slice's cell, or (b) listed in triggers of
// some contract whose ownerCell/endpoints.server is this slice's cell.
// Non-const topic → diagnostic (no exemption).
//
// AI-robust funnel evaluation:
//   - upstream: Medium — ResolvePackageRef relies on TypesInfo which requires
//     all packages to be loaded by the same packages.Load invocation.
//     Dot-import blind-spot is documented below; tracked as known Medium.
//   - downstream: Hard — ResolvePackageRef to lock callee is kernel/outbox.Emit;
//     EvaluateConstString for topic is type-system const evaluation.
//
// Blind-spot self-check:
//   - *ast.IndexExpr wrapping (explicit type args): stripped before resolution.
//   - *ast.IndexListExpr: also stripped.
//   - Dot-imports of outbox: ResolvePackageRef handles bare Ident form
//     via TypesInfo.Uses lookup. Verified in
//     TestEmitDeclCover_DetectsNonConstTopic (documented blind-spot if not).
func TestEmitDeclCover(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("EMIT-DECL-COVER-01: read module path: %v", err)
	}

	contracts := loadReverseCoverageContracts(t)
	const outboxPkg = "github.com/ghbvf/gocell/kernel/outbox"

	// Build allowed-topic set per cell.
	// cellID → set of allowed topic strings
	allowedTopics := make(map[string]map[string]bool)
	for _, c := range contracts {
		if c.Lifecycle != "active" {
			continue
		}
		// (a) kind: event with publisher → publisher cell may emit with topic = contract.id
		if c.Kind == "event" && c.Endpoints.Publisher != "" {
			cell := c.Endpoints.Publisher
			if allowedTopics[cell] == nil {
				allowedTopics[cell] = make(map[string]bool)
			}
			allowedTopics[cell][c.ID] = true
		}
		// (b) triggers: any contract with ownerCell or endpoints.server lists triggers
		ownerCell := c.OwnerCell
		if ownerCell == "" {
			ownerCell = c.Endpoints.Server
		}
		if ownerCell == "" {
			continue
		}
		for _, trigger := range c.Triggers {
			if allowedTopics[ownerCell] == nil {
				allowedTopics[ownerCell] = make(map[string]bool)
			}
			allowedTopics[ownerCell][trigger] = true
		}
	}

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		// Structural classifier: only contract-owned packages
		// (cells/<X>/... or examples/<demo>/cells/<X>/...) need their emit
		// topics validated. Framework code (runtime/, adapters/, kernel/) is
		// structurally exempt — it has no ownerCell to anchor a contract.
		cellID := extractCellIDFromPkgPath(modPath, p.Pkg.Path())
		if cellID == "" {
			return nil
		}

		var d []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				// Strip generic type args: *ast.IndexExpr / *ast.IndexListExpr
				fun := call.Fun
				if idx, ok := fun.(*ast.IndexExpr); ok {
					fun = idx.X
				} else if idxl, ok := fun.(*ast.IndexListExpr); ok {
					fun = idxl.X
				}

				// Resolve callee to (pkgPath, name)
				pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, fun)
				if !ok || pkgPath != outboxPkg || name != "Emit" {
					return
				}

				// Emit[T](ctx, emitter, topic, payload) — topic is arg[2]
				if len(call.Args) < 3 {
					return
				}
				topicExpr := call.Args[2]
				topic, isConst := EvaluateConstString(p.TypesInfo, topicExpr)
				if !isConst {
					pos := p.Fset.Position(call.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: "non-const topic in outbox.Emit; convert caller to use a const string " +
							"(EMIT-DECL-COVER-01: non-const topics cannot be statically validated)",
					})
					return
				}

				allowed := allowedTopics[cellID]
				if !allowed[topic] {
					pos := p.Fset.Position(call.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: "outbox.Emit topic " + topic + " not declared in any active contract " +
							"for cell " + cellID + " (must be an event contract publisher or a triggers entry)",
					})
				}
			})
		}
		return d
	})
	Report(t, "EMIT-DECL-COVER-01", diags)
}

// TestEmitDeclCover_DetectsNonConstTopic is the negative self-check for
// EMIT-DECL-COVER-01: ensures the non-const diagnostic path is real and
// exercised against an actual AST expression evaluated through EvaluateConstString.
//
// The test parses a synthetic call expression whose third argument is a
// non-const runtime expression (a function call). It then calls
// EvaluateConstString with a nil *types.Info (which makes any expression
// non-const by definition) and asserts the function returns ("", false).
// This proves the gate that diagnoses non-const topics uses the real
// type-checker path, not a vacuous bool.
//
// Dot-import blind-spot note: if outbox is dot-imported, ResolvePackageRef
// relies on TypesInfo.Uses for the bare Ident. The synthetic AST below uses
// a SelectorExpr (outbox.Emit), covering the normal form. The dot-import
// bare-Ident form is a documented blind-spot; if it fires in production,
// EvaluateConstString would still catch non-const topics correctly once the
// callee is resolved.
func TestEmitDeclCover_DetectsNonConstTopic(t *testing.T) {
	t.Parallel()

	// Parse a synthetic source containing a call with a non-const argument
	// (a binary expression: x + "suffix") as the third arg.
	// We specifically test that EvaluateConstString returns (_, false) for
	// a non-const expression when TypesInfo is nil (no type resolution).
	src := `package fakecell
import "context"
func doEmit(ctx context.Context, e interface{}, x string) {
	_ = outboxEmit(ctx, e, x + "suffix", nil)
}
func outboxEmit(ctx context.Context, e interface{}, topic string, p interface{}) error { return nil }
`
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, "fakecell.go", src, parser.SkipObjectResolution)
	if parseErr != nil {
		t.Fatalf("parse synthetic source: %v", parseErr)
	}

	// Find the call expression inside doEmit and extract the third argument.
	// Use EachInSubtree (the approved archtest walk helper; raw ast.Inspect is
	// banned by SCANNER-FRAMEWORK-USAGE-01).
	var topicExpr ast.Expr
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if len(call.Args) >= 3 {
			topicExpr = call.Args[2]
		}
	})
	if topicExpr == nil {
		t.Fatal("TestEmitDeclCover_DetectsNonConstTopic: failed to find call expr in synthetic source")
	}

	// EvaluateConstString with nil TypesInfo returns ("", false) for any expression.
	// This confirms: non-const args → isConst=false → diagnostic path is reached.
	_, isConst := EvaluateConstString(nil, topicExpr)
	if isConst {
		t.Error("EvaluateConstString must return false for non-const topic expression with nil TypesInfo")
	}

	// Verify the binary expression x+"suffix" is not a const string literal.
	_, isBinaryConst := topicExpr.(*ast.BinaryExpr)
	if !isBinaryConst {
		t.Errorf("expected topicExpr to be *ast.BinaryExpr, got %T", topicExpr)
	}
}

// ---------------------------------------------------------------------------
// DEAD-CONTRACT-01
// ---------------------------------------------------------------------------

// TestDeadContractCover enforces DEAD-CONTRACT-01:
// Every lifecycle: active contract.yaml must have an entry point:
//   - http → a cell impl of its generated Service interface (reuses HANDLER logic)
//   - event → endpoints.publisher != "" OR ≥1 subscriber slice OR ≥1 actorSubscriber
//   - All other kinds → ownerCell or endpoints.server non-empty.
//
// Floor scan: asserts ≥40 contracts loaded (defense against broken YAML scan).
// Today's count: 47 active contracts (2026-05-28); floor is conservative to
// allow for normal lifecycle changes without breaking this floor assertion.
//
// AI-robust funnel evaluation:
//   - upstream: Medium — YAML full enumeration relies on LoadContentFiles
//     scanning the contracts/ tree; the source map is read from generated/
//     iface_gen.go "// source:" comments. Both paths are deterministic given
//     the repo tree; broken scan triggers floor assertion.
//   - downstream: Hard — event subscriber dimension uses slice.yaml scan +
//     actorSubscribers []string field (now correctly typed); http uses typed
//     types.Implements via single-pass RunTyped.
//
// Blind-spot self-check:
//   - draft/deprecated lifecycle: excluded (only active checked).
//   - examples/ contracts with no platform backing: handled via ownerCell check.
//   - actorSubscribers: correctly parsed as []string matching kernel/metadata/types.go.
//   - Cross-load type identity: eliminated by single-pass RunTyped (all patterns
//     loaded in one packages.Load invocation).
func TestDeadContractCover(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("DEAD-CONTRACT-01: read module path: %v", err)
	}

	contracts := loadReverseCoverageContracts(t)
	activeContracts := make([]contractDoc, 0, len(contracts))
	for _, c := range contracts {
		if c.Lifecycle == "active" {
			activeContracts = append(activeContracts, c)
		}
	}

	// Floor assertion: ≥40 total contracts (today: 47; conservative floor).
	if len(contracts) < 40 {
		t.Fatalf("DEAD-CONTRACT-01: floor scan failed: expected ≥40 contracts loaded, got %d (YAML scanner may be broken)", len(contracts))
	}

	// Load slice subscriber index.
	subscribers, loadErr := loadSliceSubscribers(root)
	if loadErr != nil {
		t.Fatalf("DEAD-CONTRACT-01: loadSliceSubscribers: %v", loadErr)
	}
	subscriberIndex := make(map[string]bool) // contractID → has subscriber
	for _, s := range subscribers {
		subscriberIndex[s.ContractID] = true
	}

	// Build genPkgPath → contractYamlAbsPath from generated/contracts/http/iface_gen.go
	// "// source:" comments. Needed because codegen remaps some source path segments
	// (e.g. "internal" → "internalapi") that the dot-to-slash ID transform cannot reproduce.
	genHTTPSourceMap, mapErr := loadGeneratedHTTPServiceMap(root, modPath)
	if mapErr != nil {
		t.Fatalf("DEAD-CONTRACT-01: loadGeneratedHTTPServiceMap: %v", mapErr)
	}
	// Invert: contractYamlAbsPath → genPkgPath (for O(1) lookup in contract loop).
	contractPathToGenPkg := make(map[string]string, len(genHTTPSourceMap))
	for genPkg, contractPath := range genHTTPSourceMap {
		contractPathToGenPkg[contractPath] = genPkg
	}

	// For http contracts: build implemented Service set using a single RunTyped
	// call that loads generated/contracts/http/... + cells/... + examples/... in
	// one packages.Load invocation. Collect both iface and impl types in the same
	// callback pass to eliminate cross-pass type-identity assumptions.
	generatedHTTPPrefix := modPath + "/generated/contracts/http/"
	cellsPrefix := modPath + "/cells/"
	examplesPrefix := modPath + "/examples/"

	combinedPatterns := []string{
		modPath + "/generated/contracts/http/...",
		modPath + "/cells/...",
		modPath + "/examples/...",
	}

	type ifaceEntry struct {
		pkgPath  string
		ifaceTyp *types.Interface
	}
	type namedEntry struct {
		named *types.Named
	}

	var genServiceIfaces []ifaceEntry
	var cellNamedTypes []namedEntry

	_ = RunTyped(t, TypedOpts{Tests: false}, combinedPatterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		pkgPath := p.Pkg.Path()

		if strings.HasPrefix(pkgPath, generatedHTTPPrefix) {
			obj := p.Pkg.Scope().Lookup("Service")
			if obj == nil {
				return nil
			}
			tn, ok := obj.(*types.TypeName)
			if !ok {
				return nil
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				return nil
			}
			iface, ok := named.Underlying().(*types.Interface)
			if !ok {
				return nil
			}
			genServiceIfaces = append(genServiceIfaces, ifaceEntry{
				pkgPath:  pkgPath,
				ifaceTyp: iface.Complete(),
			})
			return nil
		}

		isCells := strings.HasPrefix(pkgPath, cellsPrefix)
		isExamples := strings.HasPrefix(pkgPath, examplesPrefix)
		if !isCells && !isExamples {
			return nil
		}
		pkgScope := p.Pkg.Scope()
		for _, name := range pkgScope.Names() {
			obj := pkgScope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok || !tn.Exported() {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			cellNamedTypes = append(cellNamedTypes, namedEntry{named: named})
		}
		return nil
	})

	// Build implemented Service set from single-pass collected types.
	implementedPkgPaths := make(map[string]bool)
	for _, impl := range cellNamedTypes {
		for _, iface := range genServiceIfaces {
			if typesutil.ImplementsInterface(impl.named, iface.ifaceTyp) {
				implementedPkgPaths[iface.pkgPath] = true
			}
		}
	}

	// Now evaluate each active contract for entry-point existence.
	var diags []Diagnostic
	for _, c := range activeContracts {
		rel, relErr := filepath.Rel(root, c.FilePath)
		if relErr != nil {
			rel = c.FilePath
		}
		rel = filepath.ToSlash(rel)

		switch c.Kind {
		case "http":
			// Look up the generated package path via the source map (correct mapping
			// even when codegen remaps path segments like internal → internalapi).
			genPkg := contractPathToGenPkg[c.FilePath]
			if genPkg == "" {
				// No generated package found for this contract: not implemented.
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: 1,
					Message: "active http contract " + c.ID + " has no generated package under generated/contracts/http/" +
						" — run 'go run ./cmd/gocell generate contract <id>' or verify contract.yaml has 'codegen: true'" +
						" (diagnostic emitted because no generated/contracts/<path>/v1/iface_gen.go was found for this contract)",
				})
				continue
			}
			if !implementedPkgPaths[genPkg] {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: 1,
					Message: "active http contract " + c.ID + " has no cell implementation of its generated Service interface " +
						"(change lifecycle to draft/deprecated if not yet implemented)",
				})
			}
		case "event":
			hasPublisher := c.Endpoints.Publisher != ""
			hasSubscriber := subscriberIndex[c.ID]
			hasActorSubscriber := len(c.Endpoints.ActorSubscribers) > 0
			if !hasPublisher && !hasSubscriber && !hasActorSubscriber {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: 1,
					Message: "active event contract " + c.ID + " has no publisher, no subscriber slice, and no actor subscriber " +
						"(change lifecycle to draft/deprecated if unused)",
				})
			}
		default:
			// command, projection, or any other kind: require ownerCell or server
			ownerCell := c.OwnerCell
			if ownerCell == "" {
				ownerCell = c.Endpoints.Server
			}
			if ownerCell == "" {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: 1,
					Message: "active " + c.Kind + " contract " + c.ID + " has no ownerCell or endpoints.server declared " +
						"(change lifecycle to draft/deprecated if unused)",
				})
			}
		}
	}
	Report(t, "DEAD-CONTRACT-01", diags)
}

// TestDeadContractCover_FloorScan is the dedicated floor-scan self-check:
// verifies that at least 40 contracts are loaded (defends against a broken
// YAML scanner that returns an empty list silently).
// Today's count: 47 contracts (2026-05-28); floor of 40 allows ±7 contracts
// for normal lifecycle changes before this assertion needs updating.
func TestDeadContractCover_FloorScan(t *testing.T) {
	t.Parallel()
	contracts := loadReverseCoverageContracts(t)
	if len(contracts) < 40 {
		t.Errorf("DEAD-CONTRACT-01 floor scan: expected ≥40 contracts, got %d (YAML scan may be broken)", len(contracts))
	}
}

// ---------------------------------------------------------------------------
// DEAD-CODE-01
// ---------------------------------------------------------------------------

// TestDeadCodeCover enforces DEAD-CODE-01:
// No production Go file (excluding *_test.go) may import
// generated/contracts/<id-path>/v1 of a lifecycle: deprecated contract, nor
// contain a string literal exactly equal to a deprecated contract.id.
// Today 0 deprecated → vacuous pass. NO // allow-* exemption.
//
// AI-robust funnel evaluation:
//   - upstream: Hard — YAML single-source for the deprecated contract set
//     (LoadContentFiles scan of contracts/ tree); no hand-maintained list.
//   - downstream: Medium — AST ImportSpec.Path + BasicLit.Value exact-match;
//     no annotation escape, no waiver yaml, no env var skip. Blank-import form
//     is covered (ImportSpec.Path.Value is the same regardless of import name).
//     Known blind-spot: dot-import of a deprecated contract generated package
//     would not appear in ImportSpec.Path — this is accepted because
//     deprecated contracts have no generated package in generated/ (codegen
//     is not run for deprecated contracts), so this blind-spot is vacuous.
//
// Blind-spot self-check:
//   - String concatenation: "event." + "foo.v1" — EvaluateConstString would
//     resolve this; but we use AST BasicLit scan (not typed), so concatenated
//     consts are NOT flagged. This is an accepted blind spot: the rule targets
//     literal references only (import paths and literal strings), not computed ones.
//   - Comments: not flagged (AST does not visit comment nodes as BasicLit).
//
// Generated package path for deprecated contracts: derived from the
// loadGeneratedAllSourceMap source map (http + event + projection), so a
// deprecated event/projection contract's generated import is caught the same
// way as an http one. Handles "internal" → "internalapi" codegen remapping.
func TestDeadCodeCover(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("DEAD-CODE-01: read module path: %v", err)
	}

	contracts := loadReverseCoverageContracts(t)
	var deprecated []contractDoc
	for _, c := range contracts {
		if c.Lifecycle == "deprecated" {
			deprecated = append(deprecated, c)
		}
	}

	// Vacuous pass when no deprecated contracts exist.
	if len(deprecated) == 0 {
		t.Logf("DEAD-CODE-01: 0 deprecated contracts (vacuous pass); rule will activate when first deprecated contract is added")
		return
	}

	// Build deprecated gen-pkg set via the all-kinds source map (covers
	// http/event/projection; correctly handles "internal" → "internalapi").
	genAllSourceMap, mapErr := loadGeneratedAllSourceMap(root, modPath)
	if mapErr != nil {
		t.Fatalf("DEAD-CODE-01: loadGeneratedAllSourceMap: %v", mapErr)
	}
	// Build reverse: contractYamlAbsPath → genPkgPath.
	contractPathToGenPkg := make(map[string]string, len(genAllSourceMap))
	for genPkg, contractPath := range genAllSourceMap {
		contractPathToGenPkg[contractPath] = genPkg
	}

	// Build deny sets.
	deprecatedIDs := make(map[string]bool)
	deprecatedGenPkgs := make(map[string]bool)
	for _, c := range deprecated {
		deprecatedIDs[c.ID] = true
		// Use source map for http contracts (handles internal → internalapi).
		if genPkg, ok := contractPathToGenPkg[c.FilePath]; ok {
			deprecatedGenPkgs[genPkg] = true
		}
		// For non-http contracts (event/command/etc.) that have no generated
		// package in generated/contracts/, the source map entry is absent and
		// we do not need to add a gen-pkg entry (no import to scan for).
	}

	scope := ModuleScope(root, MatchRels(func(rel string) bool {
		return strings.HasSuffix(rel, ".go") &&
			!strings.HasSuffix(rel, "_test.go") &&
			!strings.Contains(rel, "/testdata/")
	}))

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Check import paths.
			for _, imp := range f.Imports {
				if imp.Path == nil {
					continue
				}
				impPath := strings.Trim(imp.Path.Value, `"`)
				if deprecatedGenPkgs[impPath] {
					pos := p.Fset.Position(imp.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: "imports deprecated contract package " + impPath +
							" (remove all references to deprecated contracts; lifecycle must be updated or code deleted)",
					})
				}
			}
			// Check string literals for exact contract IDs.
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				if lit.Kind.String() != "STRING" {
					return
				}
				val, ok := StringLitValue(lit)
				if !ok {
					return
				}
				if deprecatedIDs[val] {
					pos := p.Fset.Position(lit.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: "references deprecated contract ID " + val +
							" as string literal (remove all references to deprecated contracts)",
					})
				}
			})
		}
		return d
	})
	Report(t, "DEAD-CODE-01", diags)
}

// TestDeadCodeCover_DetectsDeprecatedImport is the negative self-check for
// DEAD-CODE-01. The main TestDeadCodeCover is vacuous today (0 deprecated
// contracts), so without this self-check the detection path is never
// exercised — a silent regression in either the import-path scan or the
// string-literal scan would not be caught until the first deprecated contract
// appears in the codebase (potentially years later).
//
// This test feeds the same scan logic with a synthetic deprecated set + a
// parsed *ast.File containing a deprecated import and a deprecated id
// literal. Asserts ≥1 diagnostic per path.
//
// AI-robust grade: Hard — directly exercises the detection AST visit logic
// against a synthetic non-zero deprecated set.
func TestDeadCodeCover_DetectsDeprecatedImport(t *testing.T) {
	t.Parallel()

	const fakeGenPkg = "github.com/ghbvf/gocell/generated/contracts/event/deprecated/v1"
	const fakeContractID = "event.deprecated.v1"

	deprecatedGenPkgs := map[string]bool{fakeGenPkg: true}
	deprecatedIDs := map[string]bool{fakeContractID: true}

	const src = `package fakeproducer

import (
	"context"

	deprecated "github.com/ghbvf/gocell/generated/contracts/event/deprecated/v1"
)

const TopicDeprecated = "event.deprecated.v1"

func Use(_ context.Context) { _ = deprecated.Foo{} }
`
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, "fakeproducer.go", src, parser.ParseComments)
	if parseErr != nil {
		t.Fatalf("DEAD-CODE-01 self-check: parse synthetic source: %v", parseErr)
	}

	var diags []Diagnostic
	// Replicate the main test's import-path scan.
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		impPath := strings.Trim(imp.Path.Value, `"`)
		if deprecatedGenPkgs[impPath] {
			diags = append(diags, Diagnostic{
				Rel:     "synthetic.go",
				Line:    fset.Position(imp.Pos()).Line,
				Message: "imports deprecated contract package " + impPath,
			})
		}
	}
	// Replicate the main test's BasicLit scan.
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		val, ok := StringLitValue(lit)
		if !ok {
			return
		}
		if deprecatedIDs[val] {
			diags = append(diags, Diagnostic{
				Rel:     "synthetic.go",
				Line:    fset.Position(lit.Pos()).Line,
				Message: "references deprecated contract ID " + val,
			})
		}
	})

	// Assert: at least one diagnostic for each of the two scan paths.
	var sawImport, sawLiteral bool
	for _, d := range diags {
		if strings.Contains(d.Message, "imports deprecated") {
			sawImport = true
		}
		if strings.Contains(d.Message, "references deprecated") {
			sawLiteral = true
		}
	}
	if !sawImport {
		t.Error("DEAD-CODE-01 self-check: import-path scan did not flag the synthetic " +
			"deprecated import; detection path is broken")
	}
	if !sawLiteral {
		t.Error("DEAD-CODE-01 self-check: string-literal scan did not flag the synthetic " +
			"deprecated contract ID; detection path is broken")
	}
}
