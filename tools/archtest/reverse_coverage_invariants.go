package archtest

// reverse_coverage_invariants.go — importable Check* functions for the five
// M4-COVERAGE reverse-traceability invariants (#1638 M3 PR-8a).
//
// Non-test home so external Cell repos can import and run these rules; Go
// never compiles a dependency's _test.go. GoCell's own Test* functions in
// reverse_coverage_invariants_test.go dogfood the same Check* (single source,
// no parallel rule body).
//
// # IMPL-DECL-COVER-01
//
// Portable cell-architecture rule (cross-cell import boundary). NOT registered
// in StandardCellRules in this batch to preserve behavior-zero-change
// (registration adds new external-consumer runtime behavior beyond the
// module-path-agnostic migration); StandardCellRules registration is tracked
// holistically by epic #1302 (checklist item "凡适用外部仓库的规则进入
// StandardCellRules()"). Enforced in GoCell via TestImplDeclCover.
//
// # HANDLER-DECL-COVER-01, EMIT-DECL-COVER-01, DEAD-CONTRACT-01, DEAD-CODE-01
//
// Not registered (register=none): gocell-internal-layout/schema funnel; the
// rules depend on contract.yaml schema / generated/contracts paths / cells
// structure that do not exist in an external module. Running against an
// external repo produces vacuous-green or false-red. Enforced in GoCell via
// the corresponding Test* functions.

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/typesutil"
)

// ---------------------------------------------------------------------------
// Shared package paths (derived from PlatformModulePath)
// ---------------------------------------------------------------------------

const (
	outboxImportPath = PlatformModulePath + "/kernel/outbox"
)

// ---------------------------------------------------------------------------
// Shared contract types and loader
// ---------------------------------------------------------------------------

type contractDoc struct {
	ID        string            `yaml:"id"`
	Kind      string            `yaml:"kind"`
	Lifecycle string            `yaml:"lifecycle"`
	OwnerCell string            `yaml:"ownerCell"`
	Endpoints contractEndpoints `yaml:"endpoints"`
	Triggers  []string          `yaml:"triggers"`
	FilePath  string            `yaml:"-"`
}

type contractEndpoints struct {
	Server           string   `yaml:"server"`
	Publisher        string   `yaml:"publisher"`
	Handler          string   `yaml:"handler"`
	Provider         string   `yaml:"provider"`
	Invokers         []string `yaml:"invokers"`
	ActorSubscribers []string `yaml:"actorSubscribers"`
}

// contractProviderEndpoint returns the provider endpoint for a contract per-kind,
// mirroring kernel/metadata.ContractMeta.ProviderEndpoint exactly.
// Parity is locked by TestDeadContractCover_ProviderEndpointMirrorsMetadata.
func contractProviderEndpoint(c contractDoc) string {
	switch c.Kind {
	case "http", "grpc", "saga":
		return c.Endpoints.Server
	case "event":
		return c.Endpoints.Publisher
	case "command":
		return c.Endpoints.Handler
	case "projection":
		return c.Endpoints.Provider
	case "webhook":
		return c.OwnerCell
	default:
		return ""
	}
}

func contractProviderFieldLabel(kind string) string {
	switch kind {
	case "command":
		return "endpoints.handler"
	case "projection":
		return "endpoints.provider"
	case "event":
		return "endpoints.publisher"
	default:
		return "endpoints.server"
	}
}

var (
	contractsOnce sync.Once
	contractsAll  []contractDoc
	contractsErr  error
)

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

func loadContractDocs(root string) ([]contractDoc, error) {
	scope := DirsScope(
		root, []string{"contracts", "examples"},
		MatchRels(func(rel string) bool {
			if !strings.HasSuffix(rel, "/contract.yaml") {
				return false
			}
			if strings.HasPrefix(rel, "examples/") {
				return strings.Contains(rel, "/contracts/")
			}
			return true
		}),
	)
	files, loadErr := loadContentFiles(scope, []string{".yaml"})
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

func extractSourceComment(content []byte) string {
	for _, line := range strings.SplitN(string(content), "\n", 10) {
		const prefix = "// source: "
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// loadGeneratedSourceMap scans generated/contracts/<kindDir>/**/<fileSuffix> and
// builds genPkgPath → contractYamlAbsPath.
func loadGeneratedSourceMap(root, modPath string, kindDir, fileSuffix string) (map[string]string, error) {
	scope := DirsScope(
		root, []string{"generated/contracts/" + kindDir},
		IncludeGenerated(),
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, fileSuffix)
		}),
	)
	files, loadErr := loadContentFiles(scope, []string{".go"})
	if loadErr != nil {
		return nil, loadErr
	}
	result := make(map[string]string, len(files))
	for _, fc := range files {
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

func loadGeneratedHTTPServiceMap(root, modPath string) (map[string]string, error) {
	return loadGeneratedSourceMap(root, modPath, "http", "/iface_gen.go")
}

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
// Slice subscriber / webhook index
// ---------------------------------------------------------------------------

type sliceSubscriberEntry struct {
	BelongsToCell string
	ContractID    string
}

// loadSliceSubscribers reads slice.yaml subscribe usages from cells/ and examples/.
//
//nolint:dupl // mirrors grpc_service_in_contract; different result type
func loadSliceSubscribers(root string) ([]sliceSubscriberEntry, error) {
	scope := DirsScope(
		root, []string{"cells", "examples"},
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, "/slice.yaml")
		}),
	)
	files, loadErr := loadContentFiles(scope, []string{".yaml"})
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

func loadSliceWebhookWiring(root string) (map[string]bool, error) {
	scope := DirsScope(
		root, []string{"cells", "examples"},
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, "/slice.yaml")
		}),
	)
	files, loadErr := loadContentFiles(scope, []string{".yaml"})
	if loadErr != nil {
		return nil, loadErr
	}
	wired := make(map[string]bool)
	for _, fc := range files {
		var doc struct {
			ContractUsages []struct {
				Contract string `yaml:"contract"`
				Role     string `yaml:"role"`
			} `yaml:"contractUsages"`
		}
		if parseErr := yaml.Unmarshal(fc.Bytes, &doc); parseErr != nil {
			return nil, parseErr
		}
		for _, cu := range doc.ContractUsages {
			if cu.Contract == "" {
				continue
			}
			if cu.Role == "webhook-receive" || cu.Role == "webhook-dispatch" {
				wired[cu.Contract] = true
			}
		}
	}
	return wired, nil
}

// ---------------------------------------------------------------------------
// Cell name extraction helpers
// ---------------------------------------------------------------------------

func extractCellName(rel string) string {
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
	cellID := tail
	if next := strings.Index(tail, "/"); next >= 0 {
		cellID = tail[:next]
		rest := tail[next+1:]
		nextSeg := rest
		if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
			nextSeg = rest[:slashIdx]
		}
		if nextSeg == cellID+"test" {
			return ""
		}
	}
	return cellID
}

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

// ---------------------------------------------------------------------------
// IMPL-DECL-COVER-01
// ---------------------------------------------------------------------------

// CheckImplDeclCover enforces IMPL-DECL-COVER-01:
// Production Go files under cells/<A>/... must not import packages from a
// different cell cells/<B>/... unless the import path is under
// cells/<B>/<B>test/ (the public test helper boundary).
//
// Portable cell-architecture rule (cross-cell import boundary). NOT registered
// in StandardCellRules in this batch to preserve behavior-zero-change
// (registration adds new external-consumer runtime behavior beyond the
// module-path-agnostic migration); StandardCellRules registration is tracked
// holistically by epic #1302 (checklist item "凡适用外部仓库的规则进入
// StandardCellRules()"). Enforced in GoCell via TestImplDeclCover.
func CheckImplDeclCover(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("IMPL-DECL-COVER-01: read module path: %v", err)
	}
	cellsPrefix := modPath + "/cells/"

	scope := DirsScope(root, []string{"cells"}, MatchRels(func(rel string) bool {
		return !strings.HasSuffix(rel, "_test.go") && !strings.Contains(rel, "/testdata/")
	}))

	return Run(t, AST(scope), func(p *Pass) []Diagnostic {
		return collectImplDeclViolations(p, cellsPrefix)
	})
}

func collectImplDeclViolations(p *Pass, cellsPrefix string) []Diagnostic {
	var d []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		ownerCell := extractCellName(rel)
		if ownerCell == "" {
			continue
		}
		d = append(d, collectCrossCellImports(p, f, rel, ownerCell, cellsPrefix)...)
	}
	return d
}

func collectCrossCellImports(p *Pass, f *ast.File, rel, ownerCell, cellsPrefix string) []Diagnostic {
	var d []Diagnostic
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		impPath := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasPrefix(impPath, cellsPrefix) {
			continue
		}
		impCell := extractCellNameFromImport(cellsPrefix, impPath)
		if impCell == "" || impCell == ownerCell {
			continue
		}
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
	return d
}

// ---------------------------------------------------------------------------
// HANDLER-DECL-COVER-01
// ---------------------------------------------------------------------------

type handlerIfaceEntry struct {
	pkgPath  string
	ifaceTyp *types.Interface
	pos      token.Position
}

type handlerImplEntry struct {
	named   *types.Named
	pkgPath string
	name    string
	pos     token.Position
}

func lookupServiceIface(pkg *packages.Package) *types.Interface {
	obj := pkg.Types.Scope().Lookup("Service")
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
	return iface.Complete()
}

func collectHandlerNamedTypes(pkg *packages.Package, pkgPath string, out *[]handlerImplEntry) {
	pkgScope := pkg.Types.Scope()
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
		pos := pkg.Fset.Position(tn.Pos())
		*out = append(*out, handlerImplEntry{
			named:   named,
			pkgPath: pkgPath,
			name:    name,
			pos:     pos,
		})
	}
}

// classifyHandlerPkg classifies one package in the ModeWorkspace resolver.
// It mutates the provided slices/flag in place.
// Returns true if the caller should skip to the next package (pkg was a generated iface pkg).
func classifyHandlerPkg(
	pkg *packages.Package,
	generatedHTTPPrefix, cellsPrefix, examplesPrefix, demoExamplePrefix string,
	genServiceIfaces *[]handlerIfaceEntry,
	cellImplTypes *[]handlerImplEntry,
	sawSatelliteExample *bool,
) {
	pkgPath := pkg.PkgPath
	if strings.HasPrefix(pkgPath, generatedHTTPPrefix) {
		if iface := lookupServiceIface(pkg); iface != nil {
			obj := pkg.Types.Scope().Lookup("Service")
			pos := pkg.Fset.Position(obj.Pos())
			*genServiceIfaces = append(*genServiceIfaces, handlerIfaceEntry{
				pkgPath:  pkgPath,
				ifaceTyp: iface,
				pos:      pos,
			})
		}
		return
	}
	isCells := strings.HasPrefix(pkgPath, cellsPrefix)
	isExamples := strings.HasPrefix(pkgPath, examplesPrefix)
	if !isCells && !isExamples {
		return
	}
	if isExamples && !strings.HasPrefix(pkgPath, demoExamplePrefix) {
		*sawSatelliteExample = true
	}
	collectHandlerNamedTypes(pkg, pkgPath, cellImplTypes)
}

// collectHandlerTypeUniverse loads the ModeWorkspace type universe and returns
// (genServiceIfaces, cellImplTypes, sawSatelliteExample, error).
func collectHandlerTypeUniverse(t *testing.T, root,
	generatedHTTPPrefix, cellsPrefix, examplesPrefix string,
) ([]handlerIfaceEntry, []handlerImplEntry, bool, error) {
	t.Helper()
	modules := findWorkspaceModules(t, root)
	resolver, lpErr := typeseval.LoadProductionPackages(root, modules, false, nil)
	if lpErr != nil {
		return nil, nil, false, lpErr
	}

	var genServiceIfaces []handlerIfaceEntry
	var cellImplTypes []handlerImplEntry
	sawSatelliteExample := false
	demoExamplePrefix := examplesPrefix + "demo/"

	for _, pkg := range resolver.All() {
		if pkg == nil || pkg.Types == nil {
			continue
		}
		classifyHandlerPkg(
			pkg,
			generatedHTTPPrefix, cellsPrefix, examplesPrefix, demoExamplePrefix,
			&genServiceIfaces, &cellImplTypes, &sawSatelliteExample,
		)
	}
	return genServiceIfaces, cellImplTypes, sawSatelliteExample, nil
}

// buildActiveHTTPContractSet returns genPkgPath → true for lifecycle: active contracts.
// Input: genPkgPath → contractYamlAbsPath.
func buildActiveHTTPContractSet(genHTTPSourceMap map[string]string) map[string]bool {
	active := make(map[string]bool)
	for genPkg, contractPath := range genHTTPSourceMap {
		raw, readErr := os.ReadFile(filepath.Clean(contractPath))
		if readErr != nil {
			continue
		}
		var doc struct {
			Lifecycle string `yaml:"lifecycle"`
		}
		if yaml.Unmarshal(raw, &doc) == nil && doc.Lifecycle == "active" {
			active[genPkg] = true
		}
	}
	return active
}

func crossCheckHandlerImpls(
	root string,
	genServiceIfaces []handlerIfaceEntry,
	cellImplTypes []handlerImplEntry,
	activeHTTPContracts map[string]bool,
) []Diagnostic {
	var diags []Diagnostic
	for _, impl := range cellImplTypes {
		for _, iface := range genServiceIfaces {
			if !typesutil.ImplementsInterface(impl.named, iface.ifaceTyp) {
				continue
			}
			if !activeHTTPContracts[iface.pkgPath] {
				relPath, relErr := filepath.Rel(root, impl.pos.Filename)
				if relErr != nil || relPath == "" {
					relPath = impl.pkgPath
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
	return diags
}

// CheckHandlerDeclCover enforces HANDLER-DECL-COVER-01:
// Every concrete type in cells/* + examples/* that implements a generated
// contracts/http/.../Service interface must trace to an existing
// contracts/<id-path>/contract.yaml with lifecycle: active.
//
// Not registered (register=none): gocell-internal-layout/schema funnel;
// vacuous-green/false-red in an external module; enforced in GoCell via
// TestHandlerDeclCover.
func CheckHandlerDeclCover(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("HANDLER-DECL-COVER-01: read module path: %v", err)
	}

	genHTTPSourceMap, mapErr := loadGeneratedHTTPServiceMap(root, modPath)
	if mapErr != nil {
		t.Fatalf("HANDLER-DECL-COVER-01: loadGeneratedHTTPServiceMap: %v", mapErr)
	}
	activeHTTPContracts := buildActiveHTTPContractSet(genHTTPSourceMap)

	generatedHTTPPrefix := modPath + "/generated/contracts/http/"
	cellsPrefix := modPath + "/cells/"
	examplesPrefix := modPath + "/examples/"

	genServiceIfaces, cellImplTypes, sawSatelliteExample, universeErr := collectHandlerTypeUniverse(
		t, root, generatedHTTPPrefix, cellsPrefix, examplesPrefix,
	)
	if universeErr != nil {
		t.Fatalf("HANDLER-DECL-COVER-01: LoadProductionPackages: %v", universeErr)
	}

	if len(genServiceIfaces) == 0 {
		t.Fatal("HANDLER-DECL-COVER-01: no generated http Service interfaces found — scanner may be broken")
	}
	if !sawSatelliteExample {
		t.Fatal("HANDLER-DECL-COVER-01: workspace load surfaced no satellite example package " +
			"(examples/* beyond demo) — the ModeWorkspace loader regressed to root-only and satellite " +
			"HTTP Service impls would be invisible to this orphan-impl cross-check (#1556)")
	}

	return crossCheckHandlerImpls(root, genServiceIfaces, cellImplTypes, activeHTTPContracts)
}

// ---------------------------------------------------------------------------
// EMIT-DECL-COVER-01
// ---------------------------------------------------------------------------

// CheckEmitDeclCover enforces EMIT-DECL-COVER-01:
// Every outbox.Emit call's topic arg const string must be declared in an
// active contract (event publisher or triggers entry) for the caller's cell.
// Non-const topic args also produce a diagnostic.
//
// Not registered (register=none): gocell-internal-layout/schema funnel;
// vacuous-green/false-red in an external module; enforced in GoCell via
// TestEmitDeclCover.
func CheckEmitDeclCover(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("EMIT-DECL-COVER-01: read module path: %v", err)
	}

	contracts := loadReverseCoverageContracts(t)
	allowedTopics := buildAllowedTopicsIndex(contracts)

	return Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		return collectEmitViolations(p, modPath, allowedTopics)
	})
}

func buildAllowedTopicsIndex(contracts []contractDoc) map[string]map[string]bool {
	allowedTopics := make(map[string]map[string]bool)
	for _, c := range contracts {
		if c.Lifecycle != "active" {
			continue
		}
		addEventPublisherTopics(c, allowedTopics)
		addTriggerTopics(c, allowedTopics)
	}
	return allowedTopics
}

func addEventPublisherTopics(c contractDoc, allowedTopics map[string]map[string]bool) {
	if c.Kind != "event" || c.Endpoints.Publisher == "" {
		return
	}
	cell := c.Endpoints.Publisher
	if allowedTopics[cell] == nil {
		allowedTopics[cell] = make(map[string]bool)
	}
	allowedTopics[cell][c.ID] = true
}

func addTriggerTopics(c contractDoc, allowedTopics map[string]map[string]bool) {
	ownerCell := c.OwnerCell
	if ownerCell == "" {
		ownerCell = c.Endpoints.Server
	}
	if ownerCell == "" {
		return
	}
	for _, trigger := range c.Triggers {
		if allowedTopics[ownerCell] == nil {
			allowedTopics[ownerCell] = make(map[string]bool)
		}
		allowedTopics[ownerCell][trigger] = true
	}
}

func collectEmitViolations(p *Pass, modPath string, allowedTopics map[string]map[string]bool) []Diagnostic {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}
	cellID := extractCellIDFromPkgPath(modPath, p.Pkg.Path())
	if cellID == "" {
		return nil
	}
	var d []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		d = append(d, collectEmitCallViolations(p, f, rel, cellID, allowedTopics)...)
		d = append(d, collectEntryLiteralViolations(p, f, rel, cellID, allowedTopics)...)
	}
	return d
}

func collectEmitCallViolations(p *Pass, f *ast.File, rel, cellID string, allowedTopics map[string]map[string]bool) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		fun := call.Fun
		if idx, ok := fun.(*ast.IndexExpr); ok {
			fun = idx.X
		} else if idxl, ok := fun.(*ast.IndexListExpr); ok {
			fun = idxl.X
		}
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, fun)
		if !ok || pkgPath != outboxImportPath || name != "Emit" {
			return
		}
		if len(call.Args) < 4 {
			return
		}
		topic, isConst := EvaluateConstString(p.TypesInfo, call.Args[3])
		pos := p.Fset.Position(call.Pos())
		if !isConst {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: "non-const topic in outbox.Emit; convert caller to use a const string " +
					"(EMIT-DECL-COVER-01: non-const topics cannot be statically validated)",
			})
			return
		}
		if !allowedTopics[cellID][topic] {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: "outbox.Emit topic " + topic + " not declared in any active contract " +
					"for cell " + cellID + " (must be an event contract publisher or a triggers entry)",
			})
		}
	})
	return d
}

func collectEntryLiteralViolations(p *Pass, f *ast.File, rel, cellID string, allowedTopics map[string]map[string]bool) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.CompositeLit](f, func(lit *ast.CompositeLit) {
		typPkg, typName, ok := ResolvePackageRef(p.TypesInfo, lit.Type)
		if !ok || typPkg != outboxImportPath || typName != "Entry" {
			return
		}
		kv, found := FindFirstChild[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) bool {
			keyIdent, ok := kv.Key.(*ast.Ident)
			return ok && keyIdent.Name == "EventType"
		})
		if !found {
			return
		}
		topic, isConst := EvaluateConstString(p.TypesInfo, kv.Value)
		pos := p.Fset.Position(kv.Pos())
		if !isConst {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: "non-const EventType in outbox.Entry literal; convert to const string " +
					"(EMIT-DECL-COVER-01: non-const topics cannot be statically validated)",
			})
			return
		}
		if !allowedTopics[cellID][topic] {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: "outbox.Entry{EventType: " + topic + "} topic not declared in any " +
					"active contract for cell " + cellID + " (must be an event contract " +
					"publisher or a triggers entry)",
			})
		}
	})
	return d
}

// ---------------------------------------------------------------------------
// DEAD-CONTRACT-01
// ---------------------------------------------------------------------------

type deadContractIndexes struct {
	subscriberIndex          map[string]bool   // contractID → has subscriber slice
	webhookWiredIndex        map[string]bool   // contractID → webhook-receive or dispatch
	contractPathToHTTPGenPkg map[string]string // contractYamlAbsPath → genPkgPath (http)
	contractPathToCmdGenPkg  map[string]string // contractYamlAbsPath → genPkgPath (command)
}

func loadDeadContractIndexes(t *testing.T, root, modPath string) (*deadContractIndexes, error) {
	t.Helper()
	idx := &deadContractIndexes{}

	subs, err := loadSliceSubscribers(root)
	if err != nil {
		return nil, err
	}
	idx.subscriberIndex = make(map[string]bool)
	for _, s := range subs {
		idx.subscriberIndex[s.ContractID] = true
	}

	idx.webhookWiredIndex, err = loadSliceWebhookWiring(root)
	if err != nil {
		return nil, err
	}

	genHTTPSourceMap, err := loadGeneratedHTTPServiceMap(root, modPath)
	if err != nil {
		return nil, err
	}
	idx.contractPathToHTTPGenPkg = make(map[string]string, len(genHTTPSourceMap))
	for genPkg, contractPath := range genHTTPSourceMap {
		idx.contractPathToHTTPGenPkg[contractPath] = genPkg
	}

	genCommandSourceMap, err := loadGeneratedSourceMap(root, modPath, "command", "/command_gen.go")
	if err != nil {
		return nil, err
	}
	idx.contractPathToCmdGenPkg = make(map[string]string, len(genCommandSourceMap))
	for genPkg, contractPath := range genCommandSourceMap {
		idx.contractPathToCmdGenPkg[contractPath] = genPkg
	}

	return idx, nil
}

type dcIfaceEntry struct {
	pkgPath  string
	ifaceTyp *types.Interface
}

type dcNamedEntry struct {
	named *types.Named
}

// collectDeadContractTypeUniverse returns (genServiceIfaces, genCommandHandlerIfaces, cellNamedTypes, error).
func collectDeadContractTypeUniverse(t *testing.T, root, modPath string) ([]dcIfaceEntry, []dcIfaceEntry, []dcNamedEntry, error) {
	t.Helper()
	generatedHTTPPrefix := modPath + "/generated/contracts/http/"
	generatedCommandPrefix := modPath + "/generated/contracts/command/"
	cellsPrefix := modPath + "/cells/"
	examplesPrefix := modPath + "/examples/"

	modules := findWorkspaceModules(t, root)
	resolver, lpErr := typeseval.LoadProductionPackages(root, modules, false, nil)
	if lpErr != nil {
		return nil, nil, nil, lpErr
	}

	var genServiceIfaces []dcIfaceEntry
	var genCommandHandlerIfaces []dcIfaceEntry
	var cellNamedTypes []dcNamedEntry

	for _, pkg := range resolver.All() {
		if pkg == nil || pkg.Types == nil {
			continue
		}
		pkgPath := pkg.PkgPath
		switch {
		case strings.HasPrefix(pkgPath, generatedHTTPPrefix):
			collectDCServiceIface(pkg, pkgPath, "Service", &genServiceIfaces)
		case strings.HasPrefix(pkgPath, generatedCommandPrefix):
			collectDCServiceIface(pkg, pkgPath, "Handler", &genCommandHandlerIfaces)
		case strings.HasPrefix(pkgPath, cellsPrefix) || strings.HasPrefix(pkgPath, examplesPrefix):
			collectDCNamedTypes(pkg, &cellNamedTypes)
		}
	}
	return genServiceIfaces, genCommandHandlerIfaces, cellNamedTypes, nil
}

func collectDCServiceIface(pkg *packages.Package, pkgPath, name string, out *[]dcIfaceEntry) {
	obj := pkg.Types.Scope().Lookup(name)
	if obj == nil {
		return
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return
	}
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return
	}
	iface, ok := named.Underlying().(*types.Interface)
	if !ok {
		return
	}
	*out = append(*out, dcIfaceEntry{pkgPath: pkgPath, ifaceTyp: iface.Complete()})
}

func collectDCNamedTypes(pkg *packages.Package, out *[]dcNamedEntry) {
	pkgScope := pkg.Types.Scope()
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
		*out = append(*out, dcNamedEntry{named: named})
	}
}

// buildDeadContractImplSets returns (httpImplPkgs, cmdImplPkgs).
func buildDeadContractImplSets(
	genServiceIfaces, genCommandHandlerIfaces []dcIfaceEntry,
	cellNamedTypes []dcNamedEntry,
) (map[string]bool, map[string]bool) {
	httpImpl := make(map[string]bool)
	cmdImpl := make(map[string]bool)
	for _, impl := range cellNamedTypes {
		for _, iface := range genServiceIfaces {
			if typesutil.ImplementsInterface(impl.named, iface.ifaceTyp) {
				httpImpl[iface.pkgPath] = true
			}
		}
		for _, iface := range genCommandHandlerIfaces {
			if typesutil.ImplementsInterface(impl.named, iface.ifaceTyp) {
				cmdImpl[iface.pkgPath] = true
			}
		}
	}
	return httpImpl, cmdImpl
}

func deadContractHTTPDiag(
	c contractDoc,
	rel string,
	contractPathToHTTPGenPkg map[string]string,
	implementedPkgPaths map[string]bool,
) []Diagnostic {
	genPkg := contractPathToHTTPGenPkg[c.FilePath]
	if genPkg == "" {
		return []Diagnostic{{
			Rel:  rel,
			Line: 1,
			Message: "active http contract " + c.ID + " has no generated package under generated/contracts/http/" +
				" — run 'go run ./cmd/gocell generate contract <id>' or verify contract.yaml has 'codegen: true'" +
				" (diagnostic emitted because no generated/contracts/<path>/v1/iface_gen.go was found for this contract)",
		}}
	}
	if !implementedPkgPaths[genPkg] {
		return []Diagnostic{{
			Rel:  rel,
			Line: 1,
			Message: "active http contract " + c.ID + " has no cell implementation of its generated Service interface " +
				"(change lifecycle to draft/deprecated if not yet implemented)",
		}}
	}
	return nil
}

func deadContractEventDiag(c contractDoc, rel string, subscriberIndex map[string]bool) []Diagnostic {
	hasPublisher := c.Endpoints.Publisher != ""
	hasSubscriber := subscriberIndex[c.ID]
	hasActorSubscriber := len(c.Endpoints.ActorSubscribers) > 0
	if !hasPublisher && !hasSubscriber && !hasActorSubscriber {
		return []Diagnostic{{
			Rel:  rel,
			Line: 1,
			Message: "active event contract " + c.ID + " has no publisher, no subscriber slice, and no actor subscriber " +
				"(change lifecycle to draft/deprecated if unused)",
		}}
	}
	return nil
}

func deadContractWebhookDiag(c contractDoc, rel string, webhookWiredIndex map[string]bool) []Diagnostic {
	var diags []Diagnostic
	if c.OwnerCell == "" {
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: 1,
			Message: "active webhook contract " + c.ID + " has no ownerCell declared " +
				"(webhook provider must be the explicit ownerCell; change lifecycle to draft/deprecated if unused)",
		})
	}
	if !webhookWiredIndex[c.ID] {
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: 1,
			Message: "active webhook contract " + c.ID + " has no webhook-receive or webhook-dispatch slice wiring " +
				"(add a contractUsages[role=webhook-receive|webhook-dispatch] entry to a slice, " +
				"or change lifecycle to draft/deprecated if unused)",
		})
	}
	return diags
}

func deadContractCommandDiag(c contractDoc, rel string,
	contractPathToCmdGenPkg map[string]string, commandImplementedPkgPaths map[string]bool,
) []Diagnostic {
	var diags []Diagnostic
	if c.OwnerCell == "" && contractProviderEndpoint(c) == "" {
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: 1,
			Message: "active command contract " + c.ID + " has no ownerCell or " +
				contractProviderFieldLabel(c.Kind) + " declared " +
				"(change lifecycle to draft/deprecated if unused)",
		})
	}
	if genPkg := contractPathToCmdGenPkg[c.FilePath]; genPkg != "" && !commandImplementedPkgPaths[genPkg] {
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: 1,
			Message: "active codegen command contract " + c.ID + " has no cell implementation of its " +
				"generated Handler interface (implement the generated <pkg>.Handler in a cell/example slice " +
				"and register it via <pkg>.Register, or change lifecycle to draft/deprecated / codegen: false " +
				"if not yet implemented)",
		})
	}
	return diags
}

func deadContractDefaultDiag(c contractDoc, rel string) []Diagnostic {
	if c.OwnerCell == "" && contractProviderEndpoint(c) == "" {
		return []Diagnostic{{
			Rel:  rel,
			Line: 1,
			Message: "active " + c.Kind + " contract " + c.ID + " has no ownerCell or " +
				contractProviderFieldLabel(c.Kind) + " declared " +
				"(change lifecycle to draft/deprecated if unused)",
		}}
	}
	return nil
}

func deadContractDiagsForKind(c contractDoc, rel string, idx *deadContractIndexes,
	implementedPkgPaths, commandImplementedPkgPaths map[string]bool,
) []Diagnostic {
	switch c.Kind {
	case "http":
		return deadContractHTTPDiag(c, rel, idx.contractPathToHTTPGenPkg, implementedPkgPaths)
	case "event":
		return deadContractEventDiag(c, rel, idx.subscriberIndex)
	case "webhook":
		return deadContractWebhookDiag(c, rel, idx.webhookWiredIndex)
	case "command":
		return deadContractCommandDiag(c, rel, idx.contractPathToCmdGenPkg, commandImplementedPkgPaths)
	default:
		return deadContractDefaultDiag(c, rel)
	}
}

// CheckDeadContractCover enforces DEAD-CONTRACT-01:
// Every lifecycle: active contract.yaml must have an entry point appropriate
// for its kind.
//
// Not registered (register=none): gocell-internal-layout/schema funnel;
// vacuous-green/false-red in an external module; enforced in GoCell via
// TestDeadContractCover.
func CheckDeadContractCover(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("DEAD-CONTRACT-01: read module path: %v", err)
	}

	contracts := loadReverseCoverageContracts(t)
	if len(contracts) < 40 {
		t.Fatalf("DEAD-CONTRACT-01: floor scan failed: expected ≥40 contracts loaded, got %d (YAML scanner may be broken)", len(contracts))
	}

	var activeContracts []contractDoc
	for _, c := range contracts {
		if c.Lifecycle == "active" {
			activeContracts = append(activeContracts, c)
		}
	}

	idx, idxErr := loadDeadContractIndexes(t, root, modPath)
	if idxErr != nil {
		t.Fatalf("DEAD-CONTRACT-01: loadDeadContractIndexes: %v", idxErr)
	}

	genServiceIfaces, genCommandHandlerIfaces, cellNamedTypes, universeErr := collectDeadContractTypeUniverse(t, root, modPath)
	if universeErr != nil {
		t.Fatalf("DEAD-CONTRACT-01: LoadProductionPackages: %v", universeErr)
	}

	if len(idx.contractPathToCmdGenPkg) > 0 && len(genCommandHandlerIfaces) == 0 {
		t.Fatal("DEAD-CONTRACT-01: codegen command contracts exist but no generated Handler " +
			"interface was loaded from generated/contracts/command/* — the command Handler-impl " +
			"cross-check would pass vacuously (#1580). Check the Handler lookup + ModeWorkspace loader.")
	}

	implementedPkgPaths, commandImplementedPkgPaths := buildDeadContractImplSets(
		genServiceIfaces, genCommandHandlerIfaces, cellNamedTypes,
	)

	var diags []Diagnostic
	for _, c := range activeContracts {
		rel, relErr := filepath.Rel(root, c.FilePath)
		if relErr != nil {
			rel = c.FilePath
		}
		rel = filepath.ToSlash(rel)
		diags = append(diags, deadContractDiagsForKind(c, rel, idx,
			implementedPkgPaths, commandImplementedPkgPaths)...)
	}
	return diags
}

// ---------------------------------------------------------------------------
// DEAD-CODE-01
// ---------------------------------------------------------------------------

// CheckDeadCodeCover enforces DEAD-CODE-01:
// No production Go file may import a lifecycle: deprecated contract's generated
// package, nor contain a string literal equal to a deprecated contract.id.
// Today 0 deprecated → vacuous pass.
//
// Not registered (register=none): gocell-internal-layout/schema funnel;
// vacuous-green/false-red in an external module; enforced in GoCell via
// TestDeadCodeCover.
func CheckDeadCodeCover(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
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

	if len(deprecated) == 0 {
		t.Logf("DEAD-CODE-01: 0 deprecated contracts (vacuous pass); rule will activate when first deprecated contract is added")
		return nil
	}

	genAllSourceMap, mapErr := loadGeneratedAllSourceMap(root, modPath)
	if mapErr != nil {
		t.Fatalf("DEAD-CODE-01: loadGeneratedAllSourceMap: %v", mapErr)
	}
	contractPathToGenPkg := make(map[string]string, len(genAllSourceMap))
	for genPkg, contractPath := range genAllSourceMap {
		contractPathToGenPkg[contractPath] = genPkg
	}

	deprecatedIDs, deprecatedGenPkgs := buildDeprecatedDenySets(deprecated, contractPathToGenPkg)

	scope := ModuleScope(root, MatchRels(func(rel string) bool {
		return strings.HasSuffix(rel, ".go") &&
			!strings.HasSuffix(rel, "_test.go") &&
			!strings.Contains(rel, "/testdata/")
	}))

	return Run(t, AST(scope), func(p *Pass) []Diagnostic {
		return collectDeadCodeViolations(p, deprecatedIDs, deprecatedGenPkgs)
	})
}

func buildDeprecatedDenySets(
	deprecated []contractDoc,
	contractPathToGenPkg map[string]string,
) (ids map[string]bool, genPkgs map[string]bool) {
	ids = make(map[string]bool)
	genPkgs = make(map[string]bool)
	for _, c := range deprecated {
		ids[c.ID] = true
		if genPkg, ok := contractPathToGenPkg[c.FilePath]; ok {
			genPkgs[genPkg] = true
		}
	}
	return ids, genPkgs
}

func collectDeadCodeViolations(p *Pass, deprecatedIDs, deprecatedGenPkgs map[string]bool) []Diagnostic {
	var d []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		d = append(d, collectDeprecatedImports(p, f, rel, deprecatedGenPkgs)...)
		d = append(d, collectDeprecatedLiterals(p, f, rel, deprecatedIDs)...)
	}
	return d
}

func collectDeprecatedImports(p *Pass, f *ast.File, rel string, deprecatedGenPkgs map[string]bool) []Diagnostic {
	var d []Diagnostic
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
	return d
}

func collectDeprecatedLiterals(p *Pass, f *ast.File, rel string, deprecatedIDs map[string]bool) []Diagnostic {
	var d []Diagnostic
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
	return d
}

// ---------------------------------------------------------------------------
// metadata parity anchor (used by TestDeadContractCover_ProviderEndpointMirrorsMetadata)
// ---------------------------------------------------------------------------

// ensure metadata is imported for the parity test.
var _ = metadata.ContractMeta{}
