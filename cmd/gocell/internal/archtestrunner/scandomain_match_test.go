package archtestrunner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDomainSelectsChange exercises the matcher across every domain kind and
// the path-boundary / generated-exclusion / unknown-always-run edges.
func TestDomainSelectsChange(t *testing.T) {
	cases := []struct {
		name    string
		domain  fileDomain
		changed string
		want    bool
	}{
		// Unknown (zero value) always runs — the safety floor.
		{"unknown matches go", fileDomain{}, "framework/kernel/x.go", true},
		{"unknown matches doc", fileDomain{}, "docs/readme.md", true},
		{"unknown matches yaml", fileDomain{}, "contracts/x/v1/contract.yaml", true},

		// productionGo: any non-generated production .go.
		{"prodgo matches framework go", fileDomain{scoped: true, productionGo: true}, "framework/kernel/x.go", true},
		{"prodgo matches adapter go", fileDomain{scoped: true, productionGo: true}, "adapters/redis/client.go", true},
		{"prodgo skips generated go", fileDomain{scoped: true, productionGo: true}, "framework/generated/contracts/x.go", false},
		{"prodgo skips nested generated", fileDomain{scoped: true, productionGo: true}, "corecells/generated/x.go", false},
		{"prodgo skips testdata go", fileDomain{scoped: true, productionGo: true}, "tools/foo/testdata/x.go", false},
		{"prodgo skips markdown", fileDomain{scoped: true, productionGo: true}, "docs/readme.md", false},
		{"prodgo skips yaml", fileDomain{scoped: true, productionGo: true}, "contracts/x.yaml", false},

		// prefixes: segment-boundary match.
		{"prefix exact", fileDomain{scoped: true, prefixes: []string{"adapters/redis"}}, "adapters/redis", true},
		{"prefix under", fileDomain{scoped: true, prefixes: []string{"adapters/redis"}}, "adapters/redis/client.go", true},
		{"prefix sibling rejected", fileDomain{scoped: true, prefixes: []string{"adapters/redis"}}, "adapters/rediscluster/x.go", false},
		{"prefix other area rejected", fileDomain{scoped: true, prefixes: []string{"adapters/redis"}}, "framework/x.go", false},

		// Union: productionGo OR any prefix.
		{"union via prefix yaml", fileDomain{scoped: true, productionGo: true, prefixes: []string{"contracts"}}, "contracts/x/v1/c.yaml", true},
		{"union via prodgo", fileDomain{scoped: true, productionGo: true, prefixes: []string{"contracts"}}, "framework/x.go", true},

		// worktrees copy must not cross-match a bare top-level prefix.
		{"worktrees does not match framework prefix", fileDomain{scoped: true, prefixes: []string{"framework"}}, "worktrees/Feature/x/framework/y.go", false},

		// scoped but matches nothing relevant.
		{"scoped prefix no match", fileDomain{scoped: true, prefixes: []string{"tools/codegen/contractgen"}}, "adapters/redis/x.go", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, domainSelectsChange(tc.domain, tc.changed))
		})
	}
}

// TestIsProductionGoChange covers the production-go classifier directly.
func TestIsProductionGoChange(t *testing.T) {
	assert.True(t, isProductionGoChange("framework/kernel/auth.go"))
	assert.True(t, isProductionGoChange("corecells/accesscore/x.go"))
	assert.False(t, isProductionGoChange("framework/generated/x.go"))
	assert.False(t, isProductionGoChange("tools/x/testdata/y.go"))
	assert.False(t, isProductionGoChange("docs/readme.md"))
	assert.False(t, isProductionGoChange("contracts/x.yaml"))
	// _test.go conservatively counts as a production-go change (Tests narrowing deferred).
	assert.True(t, isProductionGoChange("framework/kernel/auth_test.go"))
}

// TestPathHasPrefix verifies segment-boundary prefix matching.
func TestPathHasPrefix(t *testing.T) {
	assert.True(t, pathHasPrefix("adapters/redis", "adapters/redis"))
	assert.True(t, pathHasPrefix("adapters/redis/client.go", "adapters/redis"))
	assert.False(t, pathHasPrefix("adapters/rediscluster/x.go", "adapters/redis"))
	assert.False(t, pathHasPrefix("framework/x.go", "adapters/redis"))
}
