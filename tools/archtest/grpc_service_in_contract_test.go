// INVARIANT: GRPC-SERVICE-IN-CONTRACT-01
//
// GRPC-SERVICE-IN-CONTRACT-01 — reg.GRPCService caller allowlist + contract
// bidirectional coverage.
//
// # Service-level granularity (formal, decided)
//
// This invariant operates at the SERVICE / CONTRACT level. This is the formal,
// decided granularity for gRPC contract enforcement in GoCell (ref: ADR
// docs/architecture/202605260000-adr-grpc-transport-adapter.md Amendment D5;
// issue #1655). The contract declares a proto SERVICE and codegen enumerates all
// its RPCs; the A/B/C checks enforce "registered grpc service's ContractID ∈
// declared grpc contracts" at service/contract granularity.
//
// The generated cellgen call is:
//
//	reg.GRPCService(cell.GRPCServiceSpec{ContractID: "grpc.foo.v1", ...})
//
// Under the hood, runtime cellScopedRegistrar.RegisterService calls the
// proto-generated Register<Svc>Server(grpc.ServiceRegistrar, impl), which maps
// EVERY RPC method of that service to the same cellID in one shot. A single
// contract.yaml declares one grpc service, covering all its RPCs.
//
// Consequence: the A/B/C enforcement here checks "registered grpc service's
// ContractID ∈ declared grpc contracts" — it does NOT guarantee that every
// individual RPC method ∈ a specific contract-declared method entry. The
// service-level contract declaration is the single source of truth; codegen
// derives the per-method registration from it.
//
// This rule has three sub-checks (A, B, C):
//
// # A — Caller allowlist (Downstream Medium)
//
// Production reg.GRPCService calls may ONLY appear in:
//   - _test.go files (seam tests / test cells)
//   - the cellgen DO-NOT-EDIT cell_gen.go ONLY (basename == cell_gen.go AND
//     the "gocell generate cell" DO-NOT-EDIT banner). Sibling generated files
//     (healthz_gen.go / slice_gen.go) carry the IDENTICAL banner but are NOT
//     sanctioned GRPCService call sites (basename pins the producer, marker
//     pins it as generated — defense in depth).
//
// Hand-written reg.GRPCService calls in business code would diverge from the
// single source of truth (slice.yaml contractUsages[role=serve]), so all such
// calls must flow through the cellgen pipeline.
//
// # B — No-orphan-reg (Coverage Medium)
//
// For every reg.GRPCService(cell.GRPCServiceSpec{ContractID: "...", ...}) call
// found in a generated cell_gen.go, the ContractID must resolve to a real
// contract.yaml with kind==grpc. Stale generated code referencing a deleted or
// renamed contract fails immediately. Enforcement is at the contract/service
// granularity — not at the individual RPC method level (see service-level note
// above). (Vacuously green today — zero kind:grpc contracts; #1151 will be the
// first consumer.)
//
// # C — No-orphan-contract (Coverage Medium)
//
// For every active kind:grpc contract with a non-empty endpoints.server, there
// must exist a slice.yaml contractUsage with role=serve referencing that
// contract ID. A contract with no serving cell is a dead declaration. Enforcement
// is at the contract/service granularity (see service-level note above).
// (Vacuously green today for the same reason.)
//
// # AI-robust grading (Funnel 双向锁评级)
//
//   - Upstream Hard (NOT implemented here): the cellgen golden test
//     TestRenderCell_GoldenGRPC byte-locks the generated reg.GRPCService(...)
//     call to the contract.yaml / slice.yaml single source of truth. Any
//     template or field change breaks the golden. The codegen funnel + golden is
//     the Hard upstream gate (ai-robust.md §Hard 范本目录 "codegen funnel + golden").
//   - Downstream Medium (A — this file): Go cannot type-gate who calls a public
//     method, so the archtest caller allowlist is the strongest achievable form.
//     Permanent ceiling tracked as won't-do gh #1631 — same family as
//     SPAN-SETATTR-HOLDER-SEAL (#851), HEALTHZ-HOLDER-SEAL (#893),
//     CTXKEYS-PRINCIPAL-WRITE-CALLER (#1282), PROJECTION-REGISTER-FUNNEL (#1372),
//     grpc-registrar-field (#1582).
//   - Coverage Medium (B, C — this file): YAML-load + go/types ContractID string
//     extraction operates at contract/service granularity. Hard path = bidirectional
//     golden lock between cellgen output and contract registry; not yet worth the
//     tooling cost (tracked as future-nice-to-have under the same gh #1631 umbrella).
//
// # Blind spots
//
//   - B1. Function-value indirection (`f := reg.GRPCService; f(spec)`): the
//     CallExpr Fun is an *ast.Ident → *types.Var, invisible to ResolveMethodCall.
//     Covered by TestGRPCServiceInContract01_ReverseBlindSpot_NoFuncValue.
//   - B2. ContractID string built at runtime (fmt.Sprintf etc.) inside a
//     generated cell_gen.go — EvaluateConstString returns ("", false) → the
//     check is skipped (conservatively not flagging). In practice cellgen always
//     emits a string literal, so this case cannot arise from the template.
//   - B3. Dot-import (`import . "github.com/ghbvf/gocell/kernel/cell"`) makes
//     GRPCService(spec) appear as a bare *ast.Ident (not *ast.SelectorExpr),
//     invisible to isGRPCServiceCall / ResolveMethodCall. Mitigated by the
//     revive dot-imports linter rule in .golangci.yml (prohibits all dot-imports
//     in production code) and by the kernel/cell import graph (cells/ and
//     examples/ do not and cannot dot-import kernel/cell due to layer rules).
//     No separate self-check test is added — the linter is the gate.
//
// ref: tools/archtest/projection_register_funnel_test.go (canonical template)
// ref: tools/archtest/reverse_coverage_invariants_test.go (loadReverseCoverageContracts)
// ref: tools/codegen/cellgen/testdata/golden/synth_grpc_cell_gen.go.golden (generated form)
// ref: kernel/cell/grpc_service.go (GRPCServiceSpec, GRPCService method)
package archtest

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const (
	// grpcServiceRegistrarPkgPath is derived from PlatformModulePath
	// (ARCHTEST-MODULE-PATH-FUNNEL-01: no bare module-path literal).
	grpcServiceRegistrarPkgPath = PlatformModulePath + "/kernel/cell"
	grpcServiceMethod           = "GRPCService"
)

// grpcCellgenMarkerLine is the DO NOT EDIT marker emitted by gocell generate
// cell into cell_gen.go / healthz_gen.go / slice_gen.go.
const grpcCellgenMarkerLine = "// Code generated by gocell generate cell. DO NOT EDIT."

// grpcFileHasCellgenMarker reports whether the file at absPath contains the
// cellgen DO NOT EDIT marker line.
func grpcFileHasCellgenMarker(absPath string) bool {
	f, err := os.Open(absPath) //nolint:gosec // archtest-internal module-root relative path
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == grpcCellgenMarkerLine {
			return true
		}
	}
	return false
}

// isGRPCServiceCall reports whether call is a method call to
// cell.Registrar.GRPCService (or *RegistryRecorder.GRPCService), resolved via
// *types.Info.
func isGRPCServiceCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != grpcServiceMethod {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != grpcServiceRegistrarPkgPath {
		return false
	}
	if fn.Name() != grpcServiceMethod {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	recv := sig.Recv()
	if recv == nil {
		return false
	}
	recvT := recv.Type()
	if ptr, ok2 := recvT.(*types.Pointer); ok2 {
		recvT = ptr.Elem()
	}
	named, ok := recvT.(*types.Named)
	if !ok {
		return false
	}
	if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != grpcServiceRegistrarPkgPath {
		return false
	}
	switch named.Obj().Name() {
	case "Registrar", "RegistryRecorder":
		return true
	default:
		return false
	}
}

// isGRPCServiceAllowed reports whether the file at the given rel/abs path may
// call reg.GRPCService:
//   - _test.go files (seam tests / test cells)
//   - the cellgen wiring file cell_gen.go ONLY, bearing the DO-NOT-EDIT marker.
//
// The marker alone is insufficient: the banner also appears in healthz_gen.go /
// slice_gen.go, so the allowlist requires basename == "cell_gen.go" AND the
// marker (basename pins the producer, marker pins it as generated).
func isGRPCServiceAllowed(rel, absPath string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	if filepath.Base(rel) == "cell_gen.go" && grpcFileHasCellgenMarker(absPath) {
		return true
	}
	return false
}

// extractGRPCContractIDs extracts every ContractID string literal from a
// reg.GRPCService(cell.GRPCServiceSpec{ContractID: "...", ...}) call within the
// given AST file. Returns nil when none are found or the ID is not a const
// literal (conservatively skipped — blind spot B2).
func extractGRPCContractIDs(f *ast.File, info *types.Info) []string {
	var ids []string
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if !isGRPCServiceCall(call, info) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		// The single argument is a cell.GRPCServiceSpec composite literal.
		lit, ok := call.Args[0].(*ast.CompositeLit)
		if !ok {
			return
		}
		EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "ContractID" {
				return
			}
			if id, ok := EvaluateConstString(info, kv.Value); ok && id != "" {
				ids = append(ids, id)
			}
		})
	})
	return ids
}

// loadSliceServeUsages scans cells/**/slice.yaml AND examples/**/slice.yaml for
// contractUsages with role=serve and returns (belongsToCell, contractID) pairs.
// This mirrors loadSliceSubscribers in reverse_coverage_invariants_test.go.
func loadSliceServeUsages(root string) ([]sliceServeEntry, error) {
	scope := DirsScope(
		root, businessCellScanDirs(),
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, "/slice.yaml")
		}),
	)

	files, loadErr := loadContentFiles(scope, []string{".yaml"})
	if loadErr != nil {
		return nil, loadErr
	}

	var entries []sliceServeEntry
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
			if cu.Role == "serve" && cu.Contract != "" {
				entries = append(entries, sliceServeEntry{
					BelongsToCell: doc.BelongsToCell,
					ContractID:    cu.Contract,
				})
			}
		}
	}
	return entries, nil
}

// sliceServeEntry describes a slice that serves a gRPC contract.
type sliceServeEntry struct {
	BelongsToCell string
	ContractID    string
}

// TestGRPCServiceInContract01_A_CallerAllowlist enforces the A sub-check of
// GRPC-SERVICE-IN-CONTRACT-01: reg.GRPCService must only be called from _test.go
// files or the cellgen DO-NOT-EDIT cell_gen.go.
//
// Status: vacuously green today (zero production GRPCService callsites); will
// become load-bearing when #1151 adds the first kind:grpc cell.
func TestGRPCServiceInContract01_A_CallerAllowlist(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	allPatterns := prodscan.Patterns(root)

	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				absPath := p.Abs(f)
				if isGRPCServiceAllowed(rel, absPath) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !isGRPCServiceCall(call, p.TypesInfo) {
						return
					}
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
						Message: fmt.Sprintf(
							"GRPC-SERVICE-IN-CONTRACT-01/A: reg.GRPCService called from "+
								"non-allowlisted file %s (line %d). Only _test.go files and the "+
								"cellgen DO-NOT-EDIT cell_gen.go may call GRPCService. cellgen "+
								"derives these from slice.yaml contractUsages[role=serve] — "+
								"hand-writing them bypasses the single source of truth.",
							rel, p.Fset.Position(call.Pos()).Line,
						),
					})
				})
			}
			return nil
		})

	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Rel != diags[j].Rel {
			return diags[i].Rel < diags[j].Rel
		}
		return diags[i].Line < diags[j].Line
	})
	Report(t, "GRPC-SERVICE-IN-CONTRACT-01/A", diags)
}

// grpcRegRef describes a single reg.GRPCService call site extracted from a
// generated cell_gen.go, carrying enough context to perform orphan detection
// without needing to re-run packages.Load.
type grpcRegRef struct {
	Rel        string // relative file path (used in Diagnostic.Rel)
	ContractID string // the ContractID string literal from the call
}

// detectOrphanRegs is the pure diagnostic core of sub-check B. It accepts the
// known set of kind:grpc contract IDs and a list of reg.GRPCService references
// found in generated cell_gen.go files, and returns one Diagnostic per
// reference whose ContractID is absent from the known set.
//
// Both the production test (TestGRPCServiceInContract01_B_NoOrphanReg) and the
// synthetic RED test (TestGRPCServiceInContract01_B_SyntheticOrphanReg) call
// this function directly — the production test drives it via the packages.Load
// code path; the synthetic test drives it via in-memory inputs.
func detectOrphanRegs(knownGRPCContracts map[string]bool, regs []grpcRegRef) []Diagnostic {
	var diags []Diagnostic
	for _, r := range regs {
		if !knownGRPCContracts[r.ContractID] {
			diags = append(diags, Diagnostic{
				Rel: r.Rel,
				Message: fmt.Sprintf(
					"GRPC-SERVICE-IN-CONTRACT-01/B: generated cell_gen.go references "+
						"ContractID %q which does not match any kind:grpc contract.yaml. "+
						"Delete or regenerate the stale entry.",
					r.ContractID,
				),
			})
		}
	}
	return diags
}

// detectOrphanContracts is the pure diagnostic core of sub-check C. It accepts
// a list of contractDocs and the set of contract IDs that have at least one
// serve contractUsage, and returns one Diagnostic per active kind:grpc contract
// with a non-empty endpoints.server that has no matching serve usage.
//
// Both the production test (TestGRPCServiceInContract01_C_NoOrphanContract) and
// the synthetic RED test (TestGRPCServiceInContract01_C_SyntheticOrphanContract)
// call this function directly.
func detectOrphanContracts(contracts []contractDoc, servedIDs map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for _, c := range contracts {
		if !isActiveGRPCContractWithServer(c) {
			continue
		}
		if !servedIDs[c.ID] {
			diags = append(diags, Diagnostic{
				Rel: c.FilePath,
				Message: fmt.Sprintf(
					"GRPC-SERVICE-IN-CONTRACT-01/C: active kind:grpc contract %q has "+
						"endpoints.server=%q but no slice.yaml declares "+
						"contractUsages[role=serve] referencing it. "+
						"Add a serve contractUsage or set lifecycle to inactive.",
					c.ID, c.Endpoints.Server,
				),
			})
		}
	}
	return diags
}

// TestGRPCServiceInContract01_B_NoOrphanReg enforces the B sub-check of
// GRPC-SERVICE-IN-CONTRACT-01: every ContractID string found in a generated
// cell_gen.go's reg.GRPCService call must resolve to a real kind:grpc contract.
//
// Status: vacuously green today (zero cell_gen.go GRPCService calls, zero
// kind:grpc contracts). Will fire if a generated cell_gen.go references a
// contract that has been deleted or renamed.
func TestGRPCServiceInContract01_B_NoOrphanReg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	contracts := loadReverseCoverageContracts(t)

	// Build a set of known kind:grpc contract IDs.
	grpcContractIDs := buildGRPCContractIDSet(contracts)

	allPatterns := prodscan.Patterns(root)
	var regs []grpcRegRef

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				absPath := p.Abs(f)
				// Only scan generated cell_gen.go files.
				if filepath.Base(rel) != "cell_gen.go" || !grpcFileHasCellgenMarker(absPath) {
					continue
				}
				for _, contractID := range extractGRPCContractIDs(f, p.TypesInfo) {
					regs = append(regs, grpcRegRef{Rel: rel, ContractID: contractID})
				}
			}
			return nil
		})

	diags := detectOrphanRegs(grpcContractIDs, regs)
	sort.Slice(diags, func(i, j int) bool {
		return diags[i].Rel < diags[j].Rel
	})
	Report(t, "GRPC-SERVICE-IN-CONTRACT-01/B", diags)
}

// buildGRPCContractIDSet returns a set of contract IDs whose kind is "grpc".
func buildGRPCContractIDSet(contracts []contractDoc) map[string]bool {
	m := make(map[string]bool)
	for _, c := range contracts {
		if c.Kind == "grpc" {
			m[c.ID] = true
		}
	}
	return m
}

// TestGRPCServiceInContract01_C_NoOrphanContract enforces the C sub-check of
// GRPC-SERVICE-IN-CONTRACT-01: every active kind:grpc contract with a non-empty
// endpoints.server must have a slice.yaml contractUsage with role=serve
// referencing its contract ID.
//
// Status: vacuously green today (zero kind:grpc contracts). Will fire if a
// kind:grpc contract is declared active with a server endpoint but no cell
// serves it.
func TestGRPCServiceInContract01_C_NoOrphanContract(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	contracts := loadReverseCoverageContracts(t)
	serveUsages, err := loadSliceServeUsages(root)
	if err != nil {
		t.Fatalf("GRPC-SERVICE-IN-CONTRACT-01/C: loadSliceServeUsages: %v", err)
	}

	// Build a set of (belongsToCell, contractID) serve pairs keyed by contractID.
	servedContracts := buildServedContractSet(serveUsages)

	diags := detectOrphanContracts(contracts, servedContracts)
	sort.Slice(diags, func(i, j int) bool {
		return diags[i].Rel < diags[j].Rel
	})
	Report(t, "GRPC-SERVICE-IN-CONTRACT-01/C", diags)
}

// isActiveGRPCContractWithServer reports whether a contract is an active
// kind:grpc contract that declares a non-empty endpoints.server.
func isActiveGRPCContractWithServer(c contractDoc) bool {
	return c.Kind == "grpc" &&
		(c.Lifecycle == "active" || c.Lifecycle == "") &&
		c.Endpoints.Server != ""
}

// buildServedContractSet builds a set of contract IDs that have at least one
// serve contractUsage across all scanned slices.
func buildServedContractSet(usages []sliceServeEntry) map[string]bool {
	m := make(map[string]bool)
	for _, u := range usages {
		m[u.ContractID] = true
	}
	return m
}

// TestGRPCServiceInContract01_B_SyntheticOrphanReg exercises detectOrphanRegs
// on in-memory synthetic data to prove the diagnostic-producing branch is
// reachable even when the repository has zero kind:grpc contracts (sub-check B
// is vacuously green in production today).
//
// RED case: a reg reference to an unknown ContractID must produce a diagnostic.
// GREEN case: a reg reference to a known ContractID must produce no diagnostic.
func TestGRPCServiceInContract01_B_SyntheticOrphanReg(t *testing.T) {
	t.Parallel()

	known := map[string]bool{
		"grpc.device.command.v1": true,
	}

	// RED: ContractID not in known set → must fire.
	redRegs := []grpcRegRef{
		{Rel: "cells/fakecell/cell_gen.go", ContractID: "grpc.nonexistent.v1"},
	}
	redDiags := detectOrphanRegs(known, redRegs)
	assert.Len(t, redDiags, 1,
		"detectOrphanRegs: unknown ContractID must produce exactly one diagnostic")
	if len(redDiags) == 1 {
		assert.Contains(t, redDiags[0].Message, "grpc.nonexistent.v1",
			"diagnostic must name the offending ContractID")
		assert.Equal(t, "cells/fakecell/cell_gen.go", redDiags[0].Rel)
	}

	// GREEN: ContractID in known set → must produce no diagnostic.
	greenRegs := []grpcRegRef{
		{Rel: "cells/fakecell/cell_gen.go", ContractID: "grpc.device.command.v1"},
	}
	greenDiags := detectOrphanRegs(known, greenRegs)
	assert.Empty(t, greenDiags,
		"detectOrphanRegs: known ContractID must produce no diagnostic")
}

// TestGRPCServiceInContract01_C_SyntheticOrphanContract exercises
// detectOrphanContracts on in-memory synthetic data to prove the
// diagnostic-producing branch is reachable even when the repository has zero
// kind:grpc contracts (sub-check C is vacuously green in production today).
//
// RED case: an active kind:grpc contract with a server but no matching serve
// usage must produce a diagnostic.
// GREEN case: an active kind:grpc contract WITH a matching serve usage must
// produce no diagnostic.
func TestGRPCServiceInContract01_C_SyntheticOrphanContract(t *testing.T) {
	t.Parallel()

	contracts := []contractDoc{
		{
			ID:        "grpc.device.command.v1",
			Kind:      "grpc",
			Lifecycle: "active",
			FilePath:  "contracts/grpc/device/command/v1/contract.yaml",
			Endpoints: contractEndpoints{Server: "devicecell"},
		},
	}

	// RED: no serve usage for this contract → must fire.
	redDiags := detectOrphanContracts(contracts, map[string]bool{})
	assert.Len(t, redDiags, 1,
		"detectOrphanContracts: unserved active grpc contract must produce exactly one diagnostic")
	if len(redDiags) == 1 {
		assert.Contains(t, redDiags[0].Message, "grpc.device.command.v1",
			"diagnostic must name the offending contract ID")
	}

	// GREEN: serve usage present → must produce no diagnostic.
	served := map[string]bool{"grpc.device.command.v1": true}
	greenDiags := detectOrphanContracts(contracts, served)
	assert.Empty(t, greenDiags,
		"detectOrphanContracts: served active grpc contract must produce no diagnostic")
}

// TestGRPCServiceInContract01_B_ASTExtractionRED proves that the real
// extractGRPCContractIDs AST extractor (banner filter + basename check +
// ContractID literal extraction) correctly surfaces a ContractID from the
// violate fixture's cell_gen.go, and that detectOrphanRegs fires when that
// ContractID is absent from the known-set.
//
// This covers the SCAN/EXTRACTION layer (packages.Load → Pass → AST walk),
// not just the pure detectOrphanRegs logic that TestGRPCServiceInContract01_B_SyntheticOrphanReg
// already exercises. A bug in extractGRPCContractIDs (banner check / basename
// filter / ContractID literal resolution) would be caught here but not in the
// synthetic test.
func TestGRPCServiceInContract01_B_ASTExtractionRED(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "grpc_service_in_contract_violate")

	// Run the real extractor over the violate fixture module.
	var extracted []grpcRegRef
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				absPath := p.Abs(f)
				// Mirror the production B-check: only scan cell_gen.go files with banner.
				if filepath.Base(rel) != "cell_gen.go" || !grpcFileHasCellgenMarker(absPath) {
					continue
				}
				for _, contractID := range extractGRPCContractIDs(f, p.TypesInfo) {
					extracted = append(extracted, grpcRegRef{Rel: rel, ContractID: contractID})
				}
			}
			return nil
		})

	// The fixture's cell_gen.go uses ContractID "grpc.fixture.v1" (see shared.go).
	assert.NotEmpty(t, extracted,
		"B AST extraction RED: extractGRPCContractIDs must find at least one ContractID "+
			"in the violate fixture's cell_gen.go (banner check + basename filter + literal eval)")

	// RED: feed extracted regs into detectOrphanRegs with a set that does NOT contain
	// "grpc.fixture.v1" → every extracted ref must produce a diagnostic.
	emptyKnown := map[string]bool{}
	diags := detectOrphanRegs(emptyKnown, extracted)
	assert.Len(t, diags, len(extracted),
		"B AST extraction RED: detectOrphanRegs must fire for every extracted ContractID "+
			"when known-set is empty")
	for _, d := range diags {
		assert.Contains(t, d.Message, "GRPC-SERVICE-IN-CONTRACT-01/B",
			"diagnostic must carry the sub-check ID")
	}
}

// TestGRPCServiceInContract01_C_YAMLScanRED proves that the real contract-YAML
// loader (loadContractDocs) and serve-usage scanner (loadSliceServeUsages)
// correctly identify an active kind:grpc contract with no serving slice.yaml,
// and that detectOrphanContracts fires for it.
//
// This covers the SCAN/EXTRACTION layer (YAML parse + DirsScope walk), not
// just the pure detectOrphanContracts logic. A bug in the YAML parser, the
// isActiveGRPCContractWithServer predicate, or the DirsScope walk would be
// caught here but not in TestGRPCServiceInContract01_C_SyntheticOrphanContract.
//
// Fixture: testdata/grpc_orphan_contract/contracts/grpc/fixture/v1/contract.yaml
// — kind:grpc, lifecycle:active, endpoints.server:fixturecell, no serving slice.
func TestGRPCServiceInContract01_C_YAMLScanRED(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureRoot := filepath.Join(root, "tools", "archtest", "testdata", "grpc_orphan_contract")

	// Run the real contract loader over the fixture root.
	contracts, err := loadContractDocs(fixtureRoot)
	if err != nil {
		t.Fatalf("C YAML scan RED: loadContractDocs: %v", err)
	}

	grpcContracts := make([]contractDoc, 0)
	for _, c := range contracts {
		if isActiveGRPCContractWithServer(c) {
			grpcContracts = append(grpcContracts, c)
		}
	}
	assert.NotEmpty(t, grpcContracts,
		"C YAML scan RED: loadContractDocs must find at least one active kind:grpc "+
			"contract with endpoints.server in the orphan fixture root")

	// Run the real serve-usage loader. The fixture root has no cells/ or examples/,
	// so servedContracts will be empty — every grpc contract is unserved.
	serveUsages, err := loadSliceServeUsages(fixtureRoot)
	if err != nil {
		t.Fatalf("C YAML scan RED: loadSliceServeUsages: %v", err)
	}
	servedContracts := buildServedContractSet(serveUsages)

	// RED: detectOrphanContracts must fire for the unserved active grpc contract.
	diags := detectOrphanContracts(contracts, servedContracts)
	assert.NotEmpty(t, diags,
		"C YAML scan RED: detectOrphanContracts must fire for the unserved active "+
			"kind:grpc contract in the fixture (grpc.fixture.orphan.v1)")
	for _, d := range diags {
		assert.Contains(t, d.Message, "GRPC-SERVICE-IN-CONTRACT-01/C",
			"diagnostic must carry the sub-check ID")
		assert.Contains(t, d.Message, "grpc.fixture.orphan.v1",
			"diagnostic must name the orphaned contract ID")
	}
}

// TestGRPCServiceInContract01_ReverseFixture loads the synthetic violation
// fixture and asserts:
//   - cell_gen.go (banner) is ALLOWED (no diagnostic)
//   - a GRPCService call in healthz_gen.go (banner, wrong basename) FIRES
//   - a GRPCService call in a non-generated rogue file FIRES
func TestGRPCServiceInContract01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "grpc_service_in_contract_violate")

	var diags []Diagnostic

	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				absPath := p.Abs(f)
				if isGRPCServiceAllowed(rel, absPath) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !isGRPCServiceCall(call, p.TypesInfo) {
						return
					}
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
					})
				})
			}
			return nil
		})

	var firedHealthz, firedCellGen, firedRogue bool
	for _, d := range diags {
		switch filepath.Base(d.Rel) {
		case "healthz_gen.go":
			firedHealthz = true
		case "cell_gen.go":
			firedCellGen = true
		case "rogue.go":
			firedRogue = true
		}
	}
	assert.False(t, firedCellGen,
		"reg.GRPCService in the sanctioned cell_gen.go (banner) must be allowed")
	assert.True(t, firedHealthz,
		"reg.GRPCService in healthz_gen.go (banner, wrong basename) MUST fire")
	assert.True(t, firedRogue,
		"reg.GRPCService in a non-generated rogue file MUST fire")
}

// TestGRPCServiceInContract01_ReverseBlindSpot_NoFuncValue (blind spot B1)
// asserts no production non-test file holds a reg.GRPCService function value
// (method expression assigned to a variable, not called), which would escape
// the SelectorExpr call detection.
func TestGRPCServiceInContract01_ReverseBlindSpot_NoFuncValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	allPatterns := prodscan.Patterns(root)

	var violations []string

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			// Collect positions of all GRPCService selectors that appear as callees.
			calleePos := make(map[interface{}]struct{})
			for _, f := range p.Files {
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != grpcServiceMethod {
						return
					}
					if isGRPCServiceCall(call, p.TypesInfo) {
						calleePos[sel.Sel] = struct{}{}
					}
				})
			}

			// Now scan for GRPCService selectors that are NOT in calleePos —
			// those are function-value usages.
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name != grpcServiceMethod {
						return
					}
					if _, isCallee := calleePos[sel.Sel]; isCallee {
						return
					}
					fn, ok := ResolveMethodCall(p.TypesInfo, sel)
					if !ok || fn == nil {
						return
					}
					if fn.Pkg() == nil || fn.Pkg().Path() != grpcServiceRegistrarPkgPath {
						return
					}
					violations = append(violations, fmt.Sprintf(
						"%s:%d: reg.GRPCService used as a function value (not a direct call) "+
							"— would escape GRPC-SERVICE-IN-CONTRACT-01/A detection (blind spot B1)",
						rel, p.Fset.Position(sel.Sel.Pos()).Line,
					))
				})
			}
			return nil
		})

	assert.Empty(t, violations,
		"blind spot B1: no production non-test file should hold a reg.GRPCService function value")
}
