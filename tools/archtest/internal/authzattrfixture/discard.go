//go:build archtest_fixture

// Package authzattrfixture is a RED fixture for AUTHZ-EVAL-ATTR-NOTFOUND-GUARD-01.
// It declares a resolve(key) ([]string, bool) method and discards the found bool
// at the callsite (fail-open) — the detector must flag it. Gated behind the
// archtest_fixture build tag so it is invisible to the Production() scan and
// loaded only by the Fixture() façade in the reverse self-check.
package authzattrfixture

type resolver struct{}

// resolve mirrors the engine's attributeResolver.resolve shape: (vals, found).
func (r resolver) resolve(key string) ([]string, bool) {
	if key == "" {
		return nil, false
	}
	return []string{key}, true
}

// DiscardFound discards the found bool (RED fail-open): a missing attribute would
// pass through as an empty value set.
func DiscardFound(key string) []string {
	r := resolver{}
	vals, _ := r.resolve(key)
	return vals
}
