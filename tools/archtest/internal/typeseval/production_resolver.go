package typeseval

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/packagesload"
	"github.com/ghbvf/gocell/tools/workspace"
)

// LoadProductionPackages loads the production package set of an entire workspace
// and partitions packages by whether their PkgPath begins with any member
// module's "<importPath>/generated/" prefix. It returns a *ProductionResolver
// whose Production() accessor exposes only the non-generated subset, eliminating
// the per-callsite IsGeneratedRelPath skip discipline.
//
// workspaceRoot is the go.work directory; modules is the set of workspace
// members (from tools/workspace.Modules — the authoritative go.work `use` set),
// each carrying its on-disk Dir and Go ImportPath. The load uses ModeWorkspace
// with one RELATIVE-DIR "./<dir>/..." pattern per member, so EVERY workspace
// module — including a nested module extracted into go.work — is scanned.
// Relative-dir patterns (not "<importPath>/..." module-path patterns) are
// required: a module-path pattern makes `go` resolve that path as an external
// dependency at its required version (network fetch), whereas a directory
// pattern resolves the local workspace member. This is the keystone that keeps
// archtest's production coverage from silently dropping an extracted module: the
// scan set is derived from go.work, not a hand-maintained list. For a
// single-module workspace (today's `use .`, Dir ".") the pattern reduces to
// "./..." — byte-identical to the former single-module load.
//
// This is the Hard-grade replacement for archtest tests that previously called
// SharedResolver(modRoot, _, _, "./..."). The PRODUCTION-LOADER-FUNNEL-01
// archtest bans the raw form in tools/archtest/*_test.go (named allowlist for
// the loader anchor test only); together with this typed accessor, a caller
// iterating pkg.Syntax cannot reach codegen output unless they explicitly opt in
// via All() — which names the trade-off at the call site.
//
// AI-robust grade: Hard for the iteration path (violation not expressible
// without renaming `Production` → `All` at every call site), Medium for the load
// API (archtest gating with named allowlist). The combination closes the
// file-level grep loophole described in
// `docs/plans/202605112000-036-archtest-governance-rollout-plan.md` §3.3.
//
// ref: charter §1 "violation not expressible" / type system funnel
// ref: golang.org/x/tools/go/analysis Pass.Files driver-controlled scope
func LoadProductionPackages(workspaceRoot string, modules []workspace.Module, tests bool, tags []string) (*ProductionResolver, error) {
	if len(modules) == 0 {
		return nil, fmt.Errorf("typeseval: LoadProductionPackages requires at least one workspace module (from tools/workspace.Modules)")
	}
	patterns := make([]string, len(modules))
	generatedPrefixes := make([]string, len(modules))
	for i, m := range modules {
		if m.ImportPath == "" || m.Dir == "" {
			return nil, fmt.Errorf("typeseval: LoadProductionPackages got an empty module dir/import path: %+v", m)
		}
		// "./<dir>/..." — relative-dir pattern resolves the local workspace
		// member; "./..." for the root module (Dir ".").
		patterns[i] = "./" + path.Join(filepath.ToSlash(m.Dir), "...")
		generatedPrefixes[i] = m.ImportPath + "/generated/"
	}
	resolver, err := sharedResolverMode(packagesload.ModeWorkspace, workspaceRoot, tests, tags, patterns...)
	if err != nil {
		return nil, fmt.Errorf("typeseval: load production packages: %w", err)
	}
	all := resolver.Packages()
	production := make([]*packages.Package, 0, len(all))
	for _, p := range all {
		if p == nil {
			continue
		}
		if hasAnyPrefix(p.PkgPath, generatedPrefixes) {
			continue
		}
		production = append(production, p)
	}
	return &ProductionResolver{all: all, production: production}, nil
}

// hasAnyPrefix reports whether s begins with any of the prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ProductionResolver partitions a real-repo "./..." load into production
// and full sets. The fields are unexported so callers cannot reach for a
// raw []*packages.Package outside of the two named accessors.
type ProductionResolver struct {
	all        []*packages.Package
	production []*packages.Package
}

// Production returns packages whose PkgPath is NOT under <module>/generated/.
// pkg.Syntax iteration over Production() cannot reach codegen output, so
// rules that reason over hand-written source can omit the per-file
// IsGeneratedRelPath skip entirely.
func (r *ProductionResolver) Production() []*packages.Package { return r.production }

// All returns the full loaded package set including generated/. Use only
// when generated/ packages are required for type resolution (e.g.,
// depgraph import-edge construction, cross-package type scope walks).
// pkg.Syntax iteration over All() WILL reach codegen output — the name
// forces callers to acknowledge that semantics at the call site.
func (r *ProductionResolver) All() []*packages.Package { return r.all }
