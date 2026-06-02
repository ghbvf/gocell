// INVARIANT: PROJECTION-CHECKPOINT-TX-BOUND-01
//
// PROJECTION-CHECKPOINT-TX-BOUND-01 — CheckpointStore ambient-tx contract.
//
// kernel/projection.CheckpointStore's SaveOffset is documented as ambient-tx:
// it participates in the caller's transaction via persistence.TxFromContext(ctx)
// rather than holding a raw *pgxpool.Pool or opening its own connection. This
// mirrors the outbox.Writer contract and the PG-REPO-AMBIENT-TX-01 pattern for
// repo/store types.
//
// This rule enforces: every concrete production type implementing
// kernel/projection.CheckpointStore must NOT directly hold a *pgxpool.Pool
// struct field. Pool-holding implementations bypass the ambient-tx contract
// and make it impossible to commit the offset advance atomically with the
// Apply mutation (the core exactly-once guarantee of PR-01).
//
// PR-01 status: vacuous-but-real pass. The only production implementation is
// MemCheckpointStore (no pool, no DB). A discovery sanity anchor asserts ≥1
// implementation was found, so a prodscan regression cannot produce a
// spurious vacuous-green.
//
// PR-02 PG adapter will add a postgres-backed CheckpointStore. That impl MUST
// hold a pgexec.PGExecutor interface (not *pgxpool.Pool) and acquire the tx
// via persistence.TxFromContext(ctx). This rule becomes load-bearing at PR-02.
//
// # AI-robust grading
//
//   - Medium (typed impl-discovery + struct field type check via *types.Info).
//     The check is stronger than a string-anchor: it uses types.Implements to
//     identify CheckpointStore implementations and *types.Info.Types to resolve
//     field type identity. It is NOT Hard because Go cannot compile-time forbid
//     a struct from declaring a *pgxpool.Pool field; the Hard upstream guarantee
//     comes from the PG-REPO-AMBIENT-TX-01 pgexec internal/pgexec/ sealed
//     sub-package (compile-time; see that rule's grading). This rule is
//     defense-in-depth on the projection layer.
//
// # Blind spots (forms *types.Info cannot see)
//
//   - B1. Global/package-level DB handle (non-field, non-parameter): a package-
//     var of type *pgxpool.Pool used inside SaveOffset would not be a struct field
//     and would escape this rule. Reverse check
//     TestProjectionCheckpointTxBound01_ReverseBlindSpot_NoPoolGlobal asserts no
//     production CheckpointStore implementation package declares a package-level
//     *pgxpool.Pool variable.
//
//   - B2. Self-opened tx: impl calls pool.BeginTx inside SaveOffset instead of
//     TxFromContext — not a field scan issue; the PG ambient-tx integration test
//     for PR-02 will catch this at runtime.
//
//   - B3. Embedded struct holding pool: an embedded struct field transitively
//     carrying *pgxpool.Pool in a CheckpointStore impl. The field scanner only
//     checks direct fields (not transitive embedding). Documented accepted
//     limitation mirroring PG-REPO-AMBIENT-TX-01 BS-1.
//
// ref: tools/archtest/pg_repo_ambient_tx_test.go (PG-REPO-AMBIENT-TX-01 R1 —
//
//	same "no raw pool in store" pattern, scoped to CheckpointStore impls)
//
// ref: kernel/projection/types.go (CheckpointStore.SaveOffset ambient-tx contract)
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §3
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	checkpointStoreIfacePkg  = "github.com/ghbvf/gocell/kernel/projection"
	checkpointStoreIfaceName = "CheckpointStore"
)

// TestProjectionCheckpointTxBound01 enforces PROJECTION-CHECKPOINT-TX-BOUND-01:
// every concrete production type implementing kernel/projection.CheckpointStore
// must not hold a *pgxpool.Pool struct field directly.
//
// PR-01 status: vacuous-but-real pass (MemCheckpointStore holds no pool).
// Discovery sanity anchor guarantees ≥1 implementation was found.
//
// # Blind spots
//
//   - B1. Package-level *pgxpool.Pool var: covered by
//     TestProjectionCheckpointTxBound01_ReverseBlindSpot_NoPoolGlobal.
//   - B2. Self-opened tx inside SaveOffset: PG integration test for PR-02.
//   - B3. Embedded struct transitively holding pool: documented accepted limitation.
func TestProjectionCheckpointTxBound01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	// kernel/ is included in prodscan.Patterns; CheckpointStore iface + MemCheckpointStore
	// are both in kernel/projection and thus in the same load.
	prodPatterns := prodscan.Patterns(root)

	var checkpointStoreIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}

			if p.Pkg.Path() == checkpointStoreIfacePkg {
				if obj := p.Pkg.Scope().Lookup(checkpointStoreIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							checkpointStoreIface = iface.Complete()
						}
					}
				}
			}

			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, checkpointStoreIface,
		"PROJECTION-CHECKPOINT-TX-BOUND-01: failed to resolve CheckpointStore interface; "+
			"check import path %s", checkpointStoreIfacePkg)

	// Discovery sanity anchor: assert ≥1 implementation was found.
	// This prevents a prodscan regression from producing a spurious vacuous-green.
	// "pkg/path.TypeName" → *types.Named
	checkpointStoreImpls := make(map[string]*types.Named)
	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectCheckpointStoreImpls(pkg, checkpointStoreIface, checkpointStoreImpls)
	}

	require.NotEmpty(t, checkpointStoreImpls,
		"PROJECTION-CHECKPOINT-TX-BOUND-01: zero CheckpointStore implementations collected — "+
			"discovery sanity anchor: at least kernel/projection.MemCheckpointStore must be found. "+
			"Likely a prodscan regression or type-universe mismatch.")

	// Scan for pool fields in each impl.
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if p.IsGenerated(f) {
					continue
				}
				EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil || ts.Name == nil {
						return
					}
					key := p.Pkg.Path() + "." + ts.Name.Name
					if _, isImpl := checkpointStoreImpls[key]; !isImpl {
						return
					}
					for _, field := range st.Fields.List {
						if !isPgxPoolType(field.Type, p.TypesInfo) {
							continue
						}
						fieldName := "_"
						if len(field.Names) > 0 {
							fieldName = field.Names[0].Name
						}
						line := p.Fset.Position(field.Type.Pos()).Line
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: line,
							Message: fmt.Sprintf(
								"PROJECTION-CHECKPOINT-TX-BOUND-01: CheckpointStore impl %s "+
									"field %s holds *pgxpool.Pool directly. "+
									"SaveOffset must use persistence.TxFromContext(ctx) to "+
									"obtain the ambient transaction — a raw pool bypasses atomic "+
									"commit with the Apply mutation (exactly-once guarantee). "+
									"Hold pgexec.PGExecutor (interface) instead; wrap the pool "+
									"via the adapter's internal/pgexec/.New factory (mirroring "+
									"PG-REPO-AMBIENT-TX-01 R1).",
								ts.Name.Name, fieldName,
							),
						})
					}
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
	Report(t, "PROJECTION-CHECKPOINT-TX-BOUND-01", diags)
}

// TestProjectionCheckpointTxBound01_RedFixture loads the synthetic violation
// fixture and asserts that the pool-field rule fires on badCheckpointStore.
//
// The fixture (internal/projectioncheckpointtxfixture) contains a struct named
// badCheckpointStore that holds a *pgxpool.Pool field. The detection logic scans
// for struct fields of type *pgxpool.Pool in types.Info — the same predicate used
// by the production rule for CheckpointStore implementations. We apply it without
// the impl-filter here (the fixture is explicitly designed to trigger the detector)
// to prove isPgxPoolType correctly identifies the field.
func TestProjectionCheckpointTxBound01_RedFixture(t *testing.T) {
	t.Parallel()

	var diags []Diagnostic

	_ = Run(
		t, Fixture(

			FixtureOpts{Tests: false},
			[]string{"./tools/archtest/internal/projectioncheckpointtxfixture/..."},
		),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil || ts.Name == nil {
						return
					}
					for _, field := range st.Fields.List {
						if !isPgxPoolType(field.Type, p.TypesInfo) {
							continue
						}
						fieldName := "_"
						if len(field.Names) > 0 {
							fieldName = field.Names[0].Name
						}
						line := p.Fset.Position(field.Type.Pos()).Line
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: line,
							Message: fmt.Sprintf(
								"PROJECTION-CHECKPOINT-TX-BOUND-01 fixture: "+
									"struct %s field %s has *pgxpool.Pool",
								ts.Name.Name, fieldName,
							),
						})
					}
				})
			}
			return nil
		},
	)

	assert.NotEmpty(t, diags,
		"PROJECTION-CHECKPOINT-TX-BOUND-01 RED fixture: expected ≥1 diagnostic for "+
			"badCheckpointStore *pgxpool.Pool field; rule logic is broken or fixture is missing")
}

// TestProjectionCheckpointTxBound01_ReverseBlindSpot_NoPoolGlobal (blind spot B1)
// asserts no production non-test CheckpointStore implementation package declares
// a package-level variable of type *pgxpool.Pool. Such a global would escape
// the struct-field scan.
func TestProjectionCheckpointTxBound01_ReverseBlindSpot_NoPoolGlobal(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	var checkpointStoreIface *types.Interface
	var implPkgPaths []string

	// First pass: collect iface + impl package paths.
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == checkpointStoreIfacePkg {
				if obj := p.Pkg.Scope().Lookup(checkpointStoreIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							checkpointStoreIface = iface.Complete()
						}
					}
				}
			}
			return nil
		})

	if checkpointStoreIface == nil {
		t.Skip("CheckpointStore interface not resolved — skip blind-spot check")
		return
	}

	// Second pass: collect impl pkg paths.
	implsByPkg := make(map[string]*types.Named)
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			collectCheckpointStoreImpls(p.Pkg, checkpointStoreIface, implsByPkg)
			return nil
		})

	seen := make(map[string]bool)
	for key := range implsByPkg {
		dotIdx := strings.LastIndex(key, ".")
		if dotIdx >= 0 {
			seen[key[:dotIdx]] = true
		}
	}
	for pkg := range seen {
		implPkgPaths = append(implPkgPaths, pkg)
	}

	var violations []string
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if !seen[p.Pkg.Path()] {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.GenDecl](f, func(gd *ast.GenDecl) {
					EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
						if vs.Type == nil {
							return
						}
						if !isPgxPoolType(vs.Type, p.TypesInfo) {
							return
						}
						line := p.Fset.Position(vs.Pos()).Line
						for _, name := range vs.Names {
							violations = append(violations, fmt.Sprintf(
								"%s:%d: package-level var %s of type *pgxpool.Pool in "+
									"CheckpointStore impl package (B1 blind spot — "+
									"PROJECTION-CHECKPOINT-TX-BOUND-01)",
								rel, line, name.Name,
							))
						}
					})
				})
			}
			return nil
		})

	assert.Empty(t, violations,
		"B1 blind-spot: no CheckpointStore impl package should have a package-level *pgxpool.Pool var; "+
			"scanned packages: %v", implPkgPaths)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectCheckpointStoreImpls discovers all concrete named types in pkg that
// implement projection.CheckpointStore (value or pointer receiver). Both
// exported and unexported types are collected.
func collectCheckpointStoreImpls(pkg *types.Package, iface *types.Interface, implSet map[string]*types.Named) {
	if iface == nil {
		return
	}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		t := obj.Type()
		if _, isIface := t.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(t, iface) {
			named, ok := t.(*types.Named)
			if !ok {
				if ptr, ok2 := t.(*types.Pointer); ok2 {
					named, ok = ptr.Elem().(*types.Named)
				}
			}
			if !ok || named == nil {
				continue
			}
			key := pkg.Path() + "." + name
			implSet[key] = named
		}
	}
}
