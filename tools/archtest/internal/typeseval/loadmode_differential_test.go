//go:build !race

// loadmode_differential_test.go — the per-package WithDeps-vs-NoDeps differential
// behind #1499. Excluded from -race builds: it is a load-mode CORRECTNESS proof, not
// a concurrency test, and the WithDeps ground-truth load is heavy under the race
// instrumentation (race-unit covers this package's concurrency via SharedResolver).
package typeseval

import (
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// differentialProbes are cross-package interface targets the archtest transitive
// (*types.Package).Imports() walks resolve (kernel/persistence.TxRunner +
// kernel/outbox.{Publisher,Writer,Emitter} — the cell raw-option rule's forbidden set,
// whose types.Implements structural-match depends on TRANSITIVE reachability rather than
// a name reference). If NoDeps export-data pruning dropped any of these from a package
// that reaches them under WithDeps, that package's structural-match would go silently
// vacuous — which the differential below makes impossible to miss.
var differentialProbes = []struct{ pkgPath, typeName string }{
	{"github.com/ghbvf/gocell/kernel/persistence", "TxRunner"},
	{"github.com/ghbvf/gocell/kernel/outbox", "Publisher"},
	{"github.com/ghbvf/gocell/kernel/outbox", "Writer"},
	{"github.com/ghbvf/gocell/kernel/outbox", "Emitter"},
}

// TestLoadMode_NoDepsPreservesTransitiveIfaceResolution is the authoritative PER-PACKAGE
// proof behind #1499: for EVERY package under ./cells/..., every probe interface that is
// transitively resolvable under the full WithDeps closure is ALSO resolvable under the
// production NoDeps loadMode. This is a genuine differential — WithDeps is the ground
// truth — so it is per-package and non-tautological (a single-mode test cannot witness
// its own pruning; any corpus statistic fails to prove a SPECIFIC package kept its
// resolution). It lives here, not in an archtest *_test.go, because the
// ARCHTEST-PASS-FUNNEL Hard-line depguard (ADR 202605141519, defense #2) bans the second
// packages.Load a differential requires; this loader package owns load-mode behavior and
// may load directly.
func TestLoadMode_NoDepsPreservesTransitiveIfaceResolution(t *testing.T) {
	root := moduleRootFromCWD(t)
	patterns := []string{"./cells/..."}

	noDeps := probeAllPackages(t, root, loadMode, patterns)                     // production NoDeps mode
	withDeps := probeAllPackages(t, root, loadMode|packages.NeedDeps, patterns) // full-closure ground truth

	var groundTruthHits int
	for pkgPath, gset := range withDeps {
		nset := noDeps[pkgPath]
		for key := range gset {
			groundTruthHits++
			require.Truef(t, nset[key],
				"package %s resolves interface %s under WithDeps but NOT under the production NoDeps "+
					"loadMode — export-data pruning dropped a transitively type-referenced interface "+
					"(the archtest cell raw-option types.Implements match would go vacuous for this package)",
				pkgPath, key)
		}
	}
	require.Positive(t, groundTruthHits,
		"WithDeps ground truth resolved no probe interface in ./cells/... — probe set or scope is wrong (vacuous test)")
}

// probeAllPackages loads patterns with mode and returns, per package import path, the set
// of "pkgPath.TypeName" probe interfaces transitively resolvable from that package.
func probeAllPackages(t *testing.T, root string, mode packages.LoadMode, patterns []string) map[string]map[string]bool {
	t.Helper()
	cfg := &packages.Config{Mode: mode, Dir: root, Tests: false}
	pkgs, err := packages.Load(cfg, patterns...)
	require.NoError(t, err, "load %v mode=%b", patterns, mode)
	require.NotEmpty(t, pkgs, "load %v returned no packages", patterns)
	out := make(map[string]map[string]bool, len(pkgs))
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		out[p.Types.Path()] = resolveProbeIfaces(p.Types)
	}
	return out
}

// resolveProbeIfaces does a transitive BFS over pkg's import closure (mirroring the shape
// of the archtest loadForbiddenIfacesFromPkg helper, replicated here because that helper
// lives in a higher layer not importable from this loader package) and returns the set of
// probe interfaces, as "pkgPath.TypeName", that resolve to a *types.Interface.
func resolveProbeIfaces(pkg *types.Package) map[string]bool {
	found := map[string]bool{}
	seen := map[string]bool{}
	queue := []*types.Package{pkg}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil || seen[cur.Path()] {
			continue
		}
		seen[cur.Path()] = true
		for _, probe := range differentialProbes {
			if cur.Path() != probe.pkgPath {
				continue
			}
			obj := cur.Scope().Lookup(probe.typeName)
			if obj == nil {
				continue
			}
			if _, ok := obj.Type().Underlying().(*types.Interface); ok {
				found[probe.pkgPath+"."+probe.typeName] = true
			}
		}
		for _, imp := range cur.Imports() {
			if !seen[imp.Path()] {
				queue = append(queue, imp)
			}
		}
	}
	return found
}

// moduleRootFromCWD resolves the module root from this package's test working directory
// (tools/archtest/internal/typeseval — four levels below the module root).
func moduleRootFromCWD(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	root := filepath.Clean(filepath.Join(cwd, "..", "..", "..", ".."))
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	require.NoErrorf(t, err, "resolved module root %s has no go.mod", root)
	return root
}
