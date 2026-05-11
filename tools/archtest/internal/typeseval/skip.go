package typeseval

import "strings"

// IsGeneratedRelPath reports whether rel points to codegen output under the
// repo's generated/ tree.
//
// Definition: rel begins with "generated/" (top-level only). The repo
// reserves exactly one generated/ directory at module root; sub-tree
// "generated/" inside a hand-written package would be a layout violation
// and is intentionally not matched.
//
// Current users: the loader anchor test
// TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3 (which
// counts generated/ files loaded by raw SharedResolver to prove the funnel
// premise). Archtest rules that previously called this helper inline have
// migrated to typeseval.LoadProductionPackages, whose ProductionResolver
// pre-filters generated/ packages at the package-set level so per-file
// IsGeneratedRelPath skipping is no longer needed in the hot path.
//
// Background: `go list ./...` includes generated/contracts/.../v1
// packages despite the legacy comment block above
// TestOutboxHandleResultFactoryPreferred claiming the opposite — the
// original PR445-FU finding F4. The Soft fix (require IsGeneratedRelPath
// presence per file) was upgraded to the Hard typed funnel in PR-SH2:
//
//   - typeseval.LoadProductionPackages / ProductionResolver provides
//     Production() (generated/-filtered) and All() (full set) accessors;
//     callers iterating pkg.Syntax cannot reach codegen output unless they
//     opt in via .All() — a named call-site signal.
//   - PRODUCTION-LOADER-FUNNEL-01 (tools/archtest/production_loader_funnel_test.go)
//     bans the raw `typeseval.SharedResolver(root, _, _, "./...")` form in
//     tools/archtest *_test.go files (named allowlist for the anchor test
//     only), so authors of new archtest rules cannot accidentally bypass
//     the funnel.
//
// Closes PR445-FU finding F4. Backlog ID
// GENERATED-SKIP-CROSS-RULE-INVARIANT-01.
func IsGeneratedRelPath(rel string) bool {
	return strings.HasPrefix(rel, "generated/")
}
