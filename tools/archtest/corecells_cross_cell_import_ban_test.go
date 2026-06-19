//go:build archtest

// INVARIANT: CORECELLS-CROSS-CELL-IMPORT-BAN-01: a corecells cell must not import
// a sibling corecells cell's Go package; cross-cell communication routes through
// contracts + the bootstrap.WithPrimaryAuthorizer / AuthorizerFromContext ctx funnel.
//
// # Why
//
// tenancy.md §ABAC authz 接线 and ADR PR-10a D1
// (docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md) state as a
// design premise that "the Authorizer lives in accesscore; auditcore/configcore
// cannot import it (cross-cell)" — it flows via the composition root
// (bootstrap.WithPrimaryAuthorizer) into request ctx, read back by
// auth.AuthorizerFromContext / auth.RequirePermission. Until now that premise had
// NO machine guard (Soft — relying on people remembering). The existing depguard
// only covers cmd/corebundle → corecells (corebundle-no-cells); corecells↔corecells
// was unguarded. This rule closes that gap, upgrading the premise Soft → Medium.
//
// Generalized beyond just the Authorizer (CLAUDE.md: "Cell 之间只通过 contract 通信"):
// a corecells cell importing ANY sibling cell's package (root provider / internal/
// service / repo / slices) couples the two cells at compile time and bypasses the
// contract boundary. Since contracts + generated code live at the repository top
// level (NOT under corecells/<cell>/), there is no legitimate cross-cell Go import,
// so the general ban has zero false positives today (verified: corecells has zero
// cross-cell imports in production AND tests as of #1981).
//
// # Scan scope (production + test + generated, exemption = build tag)
//
// Unlike CELL-TEST-NO-ADAPTER-IMPORT-01 (which governs only cell *test* files,
// because the production side is covered by the cells-isolation depguard), THIS
// rule has no depguard counterpart for corecells↔corecells (depguard would have to
// hardcode every cell pair across the separate corecells go.mod — brittle and a
// second source of truth). So it governs every .go file owned by a corecells cell:
// production, test, and generated. The exemption boundary is the ACTUAL build
// constraint (fileDefaultVisible): a file gated behind //go:build integration / e2e
// (a legitimate cross-cell integration test) is NOT visible under the default
// build context and is exempt. This is tag-name-agnostic and non-gameable — to
// escape, an author must add a build tag that removes the file from the normal
// build (a visible behavior change), exactly the boundary CELL-TEST-NO-ADAPTER-IMPORT-01
// defends.
//
// # AI-robust grading: Medium (import-path string scan; ceiling for this shape)
//
// "package A must not import package B" within a single Go module has NO
// type-system Hard form — the repo's own CELLTEST-IMPORT-BOUNDARY-01 and
// CELL-TEST-NO-ADAPTER-IMPORT-01 godocs document this. The ONLY Hard form is
// physical module separation: per-cell go.mod with an explicit require graph,
// where importing a sibling without a require entry is a compile error. corecells
// is currently ONE module (corecells/go.mod); a per-cell module split is the
// #1560 go.work follow-on EPIC, far out of scope for this guard. When/if corecells
// moves to per-cell modules, this becomes Hard for free; until then go/parser
// AST + build-constraint eval (fail-closed in CI) is the maximal carrier.
//
// This is a UNIDIRECTIONAL import ban, not a funnel — the "下游/上游 双向锁" grading
// does not apply, and no upstream sealing is reachable in Go (same ceiling depguard
// hits). Medium is made as strong as possible: (1) it runs at PR-merge time via
// hack/verify-archtest-invariants.sh (parse-only, no packages.Load — same cost
// class as the *-FUNNEL-01 import scans already in that set), not only nightly;
// (2) the synthetic FixtureMetaTest is the anti-vacuity proof that the detector
// fires; (3) scan-scope non-vacuity (corecells stays in the walk) is guarded
// upstream by PLATFORM-CELL-SCAN-COVERAGE-01; (4) the cell set is DERIVED from
// corecells/<cell>/cell.yaml presence (no Soft hardcoded cell list — a new cell is
// covered automatically).
//
// # L0 carve-out (none today)
//
// CLAUDE.md permits an L0 cell (pure-compute library) to be imported directly by a
// sibling cell in the same assembly. corecells currently has NO L0 cell (accesscore
// L3, auditcore L2, configcore L3, registrycore L1, syscore L1), so no carve-out is
// coded. If an L0 corecells cell is ever added, add an L0-target allowlist here
// (keyed on the target cell's cell.yaml consistencyLevel == "L0").
//
// # Blind spots (out of declared range) + reverse self-checks
//
//   - aliased import (acc "...corecells/accesscore/internal/ports"): COVERED — we
//     match the import path literal, not the local name (fixture: aliased_import).
//   - dot import (. "...corecells/accesscore/internal/domain"): COVERED — path
//     literal still matches (fixture: dot_import).
//   - blank import (_ "...corecells/accesscore/mem"): COVERED (fixture: blank_import).
//   - transitive import (cell A → top-level helper → cell B): OUT OF SCOPE — this
//     rule bans direct imports only; a corecells-cell-owned file in the chain would
//     itself be caught.
//   - corecells/internal/... (shared infra, e.g. testoutbox): NOT a cell (no
//     cell.yaml) — neither a valid owner nor a sibling target (fixtures:
//     shared_infra_target, shared_infra_owner_out_of_scope).
//   - the corecells module root import (github.com/ghbvf/gocell/corecells): no cell
//     segment → not a sibling (fixture: module_root_import).
//   - framework auth.Authorizer interface (framework/runtime/auth): the sanctioned
//     cross-cutting abstraction, not a cell export → allowed (fixture: framework_auth).
//   - integration/e2e build-tag gated cross-cell test: exempt (fixtures:
//     integration_gated_exempt, e2e_gated_exempt).
//
// ref: tenancy.md §ABAC authz 接线; ADR docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md §D1
// ref: CELL-TEST-NO-ADAPTER-IMPORT-01 (cell_test_no_adapter_import_test.go) — direct template
// ref: CELLTEST-IMPORT-BOUNDARY-01 (celltest_import_boundary_test.go) — import-scan gold standard
// ref: seed_role_iface.go (SEED-ROLE-IFACE) — existing fine-grained corecells-internal import ban; complementary
// ref: PLATFORM-CELL-SCAN-COVERAGE-01 — upstream scan-scope anti-vacuity for corecells
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// corecellsImportPrefix is the import path prefix of the platform-cell module
// (github.com/ghbvf/gocell/corecells). Anchored to PlatformModulePath +
// PlatformCellsDir so a module/dir rename updates exactly one place.
const corecellsImportPrefix = PlatformModulePath + "/" + PlatformCellsDir

// TestCORECELLS_CROSS_CELL_IMPORT_BAN_01 asserts no corecells cell imports a
// sibling corecells cell's Go package (production / test / generated; integration
// & e2e build-tag-gated files exempt).
func TestCORECELLS_CROSS_CELL_IMPORT_BAN_01(t *testing.T) {
	root := findModuleRoot(t)

	// Anti-vacuity: the gate is only meaningful if the DERIVED cell set is non-empty.
	// PLATFORM-CELL-SCAN-COVERAGE-01 guards that corecells stays in the file-walk
	// scope, but NOT that corecellsCellSet finds any cell — that is a separate
	// os.ReadDir + cell.yaml stat path. If every corecells/<cell>/cell.yaml ever
	// vanished, corecellsCrossCellImportFindings would return vacuously empty and
	// assert.Empty below would pass while guarding nothing. Pin the precondition here.
	cells, err := corecellsCellSet(root)
	require.NoError(t, err)
	require.NotEmpty(t, cells,
		"anti-vacuity: no corecells/<cell>/cell.yaml found — the cross-cell scan would pass vacuously")

	findings, err := corecellsCrossCellImportFindings(root)
	require.NoError(t, err)
	assert.Empty(t, findings,
		"corecells cells must not import a sibling cell's Go package; communicate via "+
			"contract + the bootstrap.WithPrimaryAuthorizer / auth.AuthorizerFromContext ctx "+
			"funnel. If this is a real cross-cell integration test that needs a live sibling, "+
			"gate it behind //go:build integration. See CORECELLS-CROSS-CELL-IMPORT-BAN-01 and "+
			"issue #1981.\nViolations:\n%s",
		strings.Join(findings, "\n"))
}

// corecellsCrossCellImportFindings walks every .go file owned by a corecells cell
// (a directory directly under corecells/ that contains a cell.yaml) that is
// visible in the default build context, and reports any direct import of a sibling
// corecells cell's package.
func corecellsCrossCellImportFindings(root string) ([]string, error) {
	cells, err := corecellsCellSet(root)
	if err != nil {
		return nil, err
	}

	files, err := ModuleScope(root, IncludeTests(), IncludeGenerated()).Files()
	if err != nil {
		return nil, err
	}

	var findings []string
	for _, path := range files {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		relSlash := filepath.ToSlash(rel)

		owner := corecellsOwningCell(relSlash, cells)
		if owner == "" {
			continue // not a file owned by a real corecells cell (cell-to-cell only)
		}

		visible, vErr := fileDefaultVisible(path)
		if vErr != nil {
			return nil, vErr
		}
		if !visible {
			continue // build-tag gated (integration/e2e/...) — legitimate cross-cell integration
		}

		imports, iErr := fileImportRefs(path)
		if iErr != nil {
			return nil, iErr
		}
		for _, ir := range imports {
			if sib := corecellsSiblingCellOf(ir.path, owner, cells); sib != "" {
				findings = append(findings, fmt.Sprintf(
					"%s:%d: imports %s (cell %s → sibling cell %s) — cross-cell Go import is banned",
					relSlash, ir.line, ir.path, owner, sib))
			}
		}
	}
	return findings, nil
}

// corecellsCellSet returns the set of corecells cell directory names: directories
// directly under corecells/ that contain a cell.yaml (CLAUDE.md: every Cell must
// have cell.yaml). Derived, not hardcoded — a new cell is covered automatically.
func corecellsCellSet(root string) (map[string]bool, error) {
	cellsDir := filepath.Join(root, PlatformCellsDir)
	entries, err := os.ReadDir(cellsDir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(cellsDir, e.Name(), "cell.yaml")); statErr == nil {
			out[e.Name()] = true
		}
	}
	return out, nil
}

// corecellsOwningCell returns the cell that owns the module-relative slash path,
// or "" if the path is not under a real corecells cell (e.g. corecells/internal/...).
func corecellsOwningCell(relSlash string, cells map[string]bool) string {
	rest, ok := strings.CutPrefix(relSlash, PlatformCellsDir+"/")
	if !ok {
		return ""
	}
	return corecellsFirstSegmentIfCell(rest, cells)
}

// corecellsSiblingCellOf returns the sibling cell named by import path imp
// (relative to owner), or "" if imp is not a sibling-cell import. Same-cell, the
// corecells module root (no cell segment), and corecells/internal/... all return "".
func corecellsSiblingCellOf(imp, owner string, cells map[string]bool) string {
	rest, ok := strings.CutPrefix(imp, corecellsImportPrefix+"/")
	if !ok {
		return ""
	}
	seg := corecellsFirstSegmentIfCell(rest, cells)
	if seg == "" || seg == owner {
		return "" // not a real cell, or same cell — both allowed
	}
	return seg
}

// corecellsFirstSegmentIfCell returns the first slash-delimited segment of rest if
// it names a real corecells cell, else "".
func corecellsFirstSegmentIfCell(rest string, cells map[string]bool) string {
	seg := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		seg = rest[:i]
	}
	if cells[seg] {
		return seg
	}
	return ""
}

// TestCORECELLS_CROSS_CELL_IMPORT_BAN_01_FixtureMetaTest verifies the detector
// classifies synthetic files correctly across the documented blind spots:
// cross-cell prod (internal & root provider), cross-cell plain test, aliased / dot
// / blank import, integration & e2e exemption, same-cell, framework auth, top-level
// generated, corecells/internal shared infra (both as target and as owner), the
// corecells module root, and a clean cross-cell-free file (reverse self-check). It
// is the anti-vacuity proof that the scanner is non-vacuous.
//
// The synthetic tree uses a representative 3-cell subset (accesscore / auditcore /
// configcore). The detector is cell-name-agnostic — it keys on cell.yaml-derived
// membership (corecellsCellSet), not a hardcoded list — so the other real cells
// (registrycore / syscore / …) are covered identically by the real-tree gate above
// (TestCORECELLS_CROSS_CELL_IMPORT_BAN_01), not re-enumerated here.
func TestCORECELLS_CROSS_CELL_IMPORT_BAN_01_FixtureMetaTest(t *testing.T) {
	t.Parallel()

	// Derive all module paths from PlatformModulePath / corecellsImportPrefix —
	// ARCHTEST-MODULE-PATH-FUNNEL-01 bans bare "github.com/ghbvf/gocell" literals
	// in archtest code.
	const (
		acc = corecellsImportPrefix + "/accesscore" // sibling target cell
		mod = PlatformModulePath                    // github.com/ghbvf/gocell
	)

	fixtures := []struct {
		name    string // also reused in the relpath assertion
		rel     string // module-relative path of the file to write
		content string
		wantHit bool
	}{
		{
			name:    "cross_cell_prod_internal",
			rel:     "corecells/auditcore/svc.go",
			content: "package auditcore\nimport _ \"" + acc + "/internal/abac\"\n",
			wantHit: true,
		},
		{
			name:    "cross_cell_prod_root_provider",
			rel:     "corecells/configcore/x.go",
			content: "package configcore\nimport _ \"" + acc + "\"\n",
			wantHit: true,
		},
		{
			name:    "cross_cell_plain_test",
			rel:     "corecells/auditcore/svc_test.go",
			content: "package auditcore\nimport _ \"" + acc + "/slices/authorizationdecide\"\n",
			wantHit: true,
		},
		{
			name:    "integration_gated_exempt",
			rel:     "corecells/auditcore/it_test.go",
			content: "//go:build integration\n\npackage auditcore\nimport _ \"" + acc + "/internal/abac\"\n",
			wantHit: false,
		},
		{
			name:    "e2e_gated_exempt",
			rel:     "corecells/configcore/e2e_test.go",
			content: "//go:build e2e\n\npackage configcore\nimport _ \"" + acc + "/mem\"\n",
			wantHit: false,
		},
		{
			name:    "aliased_import",
			rel:     "corecells/configcore/alias.go",
			content: "package configcore\nimport accports \"" + acc + "/internal/ports\"\nvar _ = accports.Nil\n",
			wantHit: true,
		},
		{
			name:    "dot_import",
			rel:     "corecells/auditcore/dot.go",
			content: "package auditcore\nimport . \"" + acc + "/internal/domain\"\n",
			wantHit: true,
		},
		{
			name:    "blank_import",
			rel:     "corecells/auditcore/blank.go",
			content: "package auditcore\nimport _ \"" + acc + "/mem\"\n",
			wantHit: true,
		},
		{
			name:    "same_cell_allowed",
			rel:     "corecells/accesscore/own.go",
			content: "package accesscore\nimport _ \"" + acc + "/internal/abac\"\n",
			wantHit: false,
		},
		{
			name:    "framework_auth_allowed",
			rel:     "corecells/auditcore/fw.go",
			content: "package auditcore\nimport _ \"" + mod + "/framework/runtime/auth\"\n",
			wantHit: false,
		},
		{
			name:    "top_level_generated_allowed",
			rel:     "corecells/auditcore/gen.go",
			content: "package auditcore\nimport _ \"" + mod + "/generated/contracts/foo\"\n",
			wantHit: false,
		},
		{
			name:    "shared_infra_target_allowed",
			rel:     "corecells/auditcore/shared.go",
			content: "package auditcore\nimport _ \"" + corecellsImportPrefix + "/internal/testoutbox\"\n",
			wantHit: false,
		},
		{
			name:    "module_root_import_allowed",
			rel:     "corecells/configcore/root.go",
			content: "package configcore\nimport _ \"" + corecellsImportPrefix + "\"\n",
			wantHit: false,
		},
		{
			// corecells/internal/ is shared infra, not a cell — a file there importing
			// a cell is OUT OF SCOPE (this rule is cell↔cell only).
			name:    "shared_infra_owner_out_of_scope",
			rel:     "corecells/internal/testoutbox/x.go",
			content: "package testoutbox\nimport _ \"" + acc + "/internal/abac\"\n",
			wantHit: false,
		},
		{
			// Reverse self-check: a file importing only stdlib / framework / same-cell
			// must NOT be flagged (no false positive).
			name:    "clean_no_cross_cell",
			rel:     "corecells/auditcore/clean_test.go",
			content: "package auditcore\nimport (\n\t\"testing\"\n\t_ \"" + mod + "/framework/kernel/outbox\"\n)\nfunc TestClean(t *testing.T) {}\n",
			wantHit: false,
		},
	}

	root := t.TempDir()
	// Mark accesscore / auditcore / configcore as real cells via cell.yaml so the
	// derived cell set recognizes them.
	for _, cell := range []string{"accesscore", "auditcore", "configcore"} {
		dir := filepath.Join(root, "corecells", cell)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cell.yaml"), []byte("id: "+cell+"\n"), 0o644))
	}
	for _, fx := range fixtures {
		full := filepath.Join(root, filepath.FromSlash(fx.rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(fx.content), 0o644))
	}

	findings, err := corecellsCrossCellImportFindings(root)
	require.NoError(t, err)

	for _, fx := range fixtures {
		hit := false
		for _, f := range findings {
			if strings.HasPrefix(f, fx.rel+":") {
				hit = true
				break
			}
		}
		assert.Equal(t, fx.wantHit, hit, "fixture %s: cross-cell-import detection result", fx.name)
	}

	// Line-number self-check: the cross_cell_prod_internal finding must point at the
	// real import line (2), proving line tracking is not hardcoded.
	var prodInternal string
	for _, f := range findings {
		if strings.HasPrefix(f, "corecells/auditcore/svc.go:") {
			prodInternal = f
			break
		}
	}
	require.NotEmpty(t, prodInternal, "cross_cell_prod_internal must be flagged")
	assert.True(t, strings.HasPrefix(prodInternal, "corecells/auditcore/svc.go:2:"),
		"finding must point at the real import line (2), got %q", prodInternal)
}
