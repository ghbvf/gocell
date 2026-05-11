package typeseval

import (
	"fmt"
	"strings"

	"golang.org/x/tools/go/packages"
)

// LoadProductionPackages loads modRoot's "./..." package set via
// SharedResolver and partitions packages by whether their PkgPath begins
// with <modulePath>/generated/. It returns a *ProductionResolver whose
// Production() accessor exposes only the non-generated subset, eliminating
// the per-callsite IsGeneratedRelPath skip discipline.
//
// This is the Hard-grade replacement for archtest tests that previously
// called SharedResolver(modRoot, _, _, "./..."). The PRODUCTION-LOADER-FUNNEL-01
// archtest bans the raw form in tools/archtest/*_test.go (named allowlist
// for the loader anchor test only); together with this typed accessor, a
// caller iterating pkg.Syntax cannot reach codegen output unless they
// explicitly opt in via All() — which names the trade-off at the call site.
//
// AI-rebust grade: Hard for the iteration path (violation not expressible
// without renaming `Production` → `All` at every call site), Medium for
// the load API (archtest gating with named allowlist). The combination
// closes the file-level grep loophole described in
// `docs/plans/202605112000-036-archtest-governance-rollout-plan.md` §3.3.
//
// ref: charter §1 "violation not expressible" / type system funnel
// ref: golang.org/x/tools/go/analysis Pass.Files driver-controlled scope
func LoadProductionPackages(modRoot, modulePath string, tests bool, tags []string) (*ProductionResolver, error) {
	if modulePath == "" {
		return nil, fmt.Errorf("typeseval: LoadProductionPackages requires non-empty modulePath (read from go.mod)")
	}
	resolver, err := SharedResolver(modRoot, tests, tags, "./...")
	if err != nil {
		return nil, fmt.Errorf("typeseval: load production packages: %w", err)
	}
	generatedPrefix := modulePath + "/generated/"
	all := resolver.Packages()
	production := make([]*packages.Package, 0, len(all))
	for _, p := range all {
		if p == nil {
			continue
		}
		if strings.HasPrefix(p.PkgPath, generatedPrefix) {
			continue
		}
		production = append(production, p)
	}
	return &ProductionResolver{all: all, production: production}, nil
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
