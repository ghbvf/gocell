package archtestrunner

import (
	"strings"
)

// scandomain.go derives, per archtest rule (test function), the set of source
// files that rule scans, so `gocell verify archtest --changed` can map a
// changed source file to the rules it could affect (gh #1877) instead of only
// re-running rules whose own *_test.go file changed (the #1563 mechanical
// version).
//
// The scan domain is recovered by static AST analysis of the tools/archtest
// package: each rule expresses its scope through a scope constructor
// (Production / Typed / DirsScope / ModuleScope / Fixture / StandaloneModule),
// often inside a companion CheckXxx func reached via Report(t, rule, CheckXxx(...)).
// This is an approximation, not an exact scan trace — when a scope argument is
// computed (a non-literal var, an unresolved helper, a whole-module scope) the
// rule's domain is left UNKNOWN and the rule always runs. --changed is a fast
// pre-filter, never the merge gate (the full sharded run is authoritative), so
// over-running is safe and under-running (a false negative) is the only failure
// that matters — the zero-value semantics below make that unrepresentable.

// fileDomain describes the source-file domain a single archtest rule scans.
//
// SAFETY (Hard, zero-value semantics): the zero value fileDomain{} has
// scoped==false, which domainSelectsChange treats as "unknown => always run".
// Every path that cannot statically determine a rule's scope — a parse failure,
// an unrecognized scope constructor, a computed (non-literal) scope argument, or
// a test func absent from the index (missing map key) — therefore falls back to
// the always-run zero value. Skipping a rule requires actively producing a
// scoped fileDomain whose matchers reject the change; it cannot happen by
// omission. This keeps --changed source-mode free of false negatives.
type fileDomain struct {
	// scoped is false (the zero value) when the rule's scan domain could not be
	// statically determined; such rules always run (conservative).
	scoped bool
	// productionGo is true when the rule scans all non-generated production Go
	// (the Production(...) scope); any production .go change selects it.
	productionGo bool
	// prefixes are repo-relative, slash-separated path prefixes (no leading
	// "./", no trailing "/..." or "/") the rule scans; a changed file at or
	// under any prefix selects the rule.
	prefixes []string
}

// domainSelectsChange reports whether a changed repo-relative file (slash form)
// could affect a rule with the given domain.
//
// Unknown (unscoped) domains always select (safety). A productionGo domain
// selects any non-generated production .go change. A prefix selects a change at
// or under it.
func domainSelectsChange(d fileDomain, changedRepoRelPath string) bool {
	if !d.scoped {
		return true // unknown => always run (no false negatives)
	}
	if d.productionGo && isProductionGoChange(changedRepoRelPath) {
		return true
	}
	for _, p := range d.prefixes {
		if pathHasPrefix(changedRepoRelPath, p) {
			return true
		}
	}
	return false
}

// isProductionGoChange reports whether a changed path is a production Go file
// that a Production(...) scan would load: a .go file not under any generated/
// or testdata/ segment.
//
// Note: _test.go files are conservatively counted as production Go changes here
// — Production(TypedOpts{Tests:false}) does not load test files, but narrowing
// on the per-call Tests flag is deferred (gh #1877 follow-up); over-running a
// Production rule on a _test.go edit is safe.
func isProductionGoChange(p string) bool {
	if !strings.HasSuffix(p, ".go") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "generated" || seg == "testdata" {
			return false
		}
	}
	return true
}

// pathHasPrefix reports whether changed is at or under the repo-relative prefix
// (segment-boundary match, so "adapters/redis" does not match
// "adapters/rediscluster/...").
func pathHasPrefix(changed, prefix string) bool {
	return changed == prefix || strings.HasPrefix(changed, prefix+"/")
}

// buildFileDomainIndex returns a map from archtest test function name to its
// scan domain, derived from a static analysis of the tools/archtest package.
//
// STUB (RED): the real implementation lands in the GREEN commit. Returning an
// empty map means every test func resolves to the zero-value (unknown) domain,
// i.e. always run — the safe default while the analysis is unimplemented.
func buildFileDomainIndex(workspaceRoot string) (map[string]fileDomain, error) {
	_ = workspaceRoot
	return map[string]fileDomain{}, nil
}
