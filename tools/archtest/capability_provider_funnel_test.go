// INVARIANT: CAPABILITY-PROVIDER-FUNNEL-01
//
// # CAPABILITY-PROVIDER-FUNNEL-01
//
// The shared-infrastructure constructors
//
//	adapters/postgres.NewPool / NewTxManager / NewOutboxWriter
//	adapters/redis.NewClient
//
// may only be invoked from the assembly's single provisioning site
// (cmd/corebundle/cap_wiring.go). Cell module files (cmd/<assembly>/*_module.go)
// and every other cmd/ file must consume the injected capability.PGProvider /
// capability.RedisProvider instead of constructing the shared pool / client / the
// pool-bound TxManager+OutboxWriter themselves.
//
// This is the downstream half of the funnel; the upstream half is the
// sealed capability.PGProvider / capability.RedisProvider (unexported marker methods +
// private impls + sole NewPGProvider/NewRedisProvider constructors in
// runtime/capability), which Go's type system makes unforgeable outside package capability.
//
// # Not banned (per-cell / per-role derivations)
//
// adapters/postgres.NewSessionStore / NewRefreshStore / NewLedgerStore /
// NewOutboxStore and adapters/redis.NewCache / NewRedisDriver /
// NewIdempotencyClaimer / NewNonceStore take an already-acquired pool handle
// (*pgxpool.Pool) or *redis.Client and derive a per-cell / per-role object.
// They are the legitimate downstream of the provider (CLAUDE.md observability
// §"per-cell 资源：cell 直接构造 NewCache(client, …)") and stay in module files.
//
// # AI-robust: upstream Hard + downstream Medium (transition form)
//
// Upstream is Hard (sealed construction, type system). Downstream is Medium:
// archtest resolves every CallExpr callee via archtest.ResolvePackageRef
// (owning package import path, not the source Ident), matching by (pkgPath,
// name). Per ai-robust.md §"Funnel 双向锁评级" a Hard-upstream + Medium-downstream
// funnel is an allowed transition form; the downstream→Hard upgrade
// (cmd/corebundle module files moved out of `package main` so the banned
// constructors are import-unreachable) is tracked at gh issue #988.
//
// # _test.go scope
//
// RunTyped(opts.Tests=false) loads only production-variant packages, so
// _test.go files are not in pass.Files; the scanner additionally filters by
// rel suffix.
//
// # Blind spots (BS)
//
//   - BS-1 Name shadowing: a non-adapter package exporting a function literally
//     named NewPool does NOT match — ResolvePackageRef compares the callee's
//     owning package path via go/types, not the import-site Ident.
//   - BS-2 Function-value indirection: `var f = adapterredis.NewClient; f(...)`
//     resolves the call's callee to a *types.Var (ok=false) and is not flagged.
//     Accepted: this is exactly how cmd/corebundle/redis.go injects the client
//     factory (redisClientFactory), and it lives in the composition root, not a
//     module file. Same accepted BS as CAS-PROTOCOL-COMPOSITION-ROOT-01 BS-2.
//   - BS-3 Reflection construction: out of scope per ai-robust.md §3.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	capFunnelRuleID    = "CAPABILITY-PROVIDER-FUNNEL-01"
	capPgImportPath    = "github.com/ghbvf/gocell/adapters/postgres"
	capRedisImportPath = "github.com/ghbvf/gocell/adapters/redis"
	// capWiringRel is the sole sanctioned provisioning site.
	capWiringRel = "cmd/corebundle/cap_wiring.go"
)

// capForbiddenCtors maps each shared-infrastructure adapter package to the
// closed set of constructors banned outside the provisioning site. Per-cell /
// per-role derivations (NewSessionStore, NewCache, …) are intentionally absent.
var capForbiddenCtors = map[string]map[string]struct{}{
	capPgImportPath: {
		"NewPool":         {},
		"NewTxManager":    {},
		"NewOutboxWriter": {},
	},
	capRedisImportPath: {
		"NewClient": {},
	},
}

// TestCapabilityProviderFunnel_CompositionRootOnly enforces
// CAPABILITY-PROVIDER-FUNNEL-01: the shared-infra adapter constructors may only
// be called from cmd/corebundle/cap_wiring.go. Cell module files must consume
// the injected capability.PGProvider / capability.RedisProvider.
func TestCapabilityProviderFunnel_CompositionRootOnly(t *testing.T) {
	diags := RunTyped(t,
		TypedOpts{Tests: false},
		[]string{"./cmd/..."},
		scanCapabilityProviderViolations,
	)
	Report(t, capFunnelRuleID, diags)
}

// scanCapabilityProviderViolations walks every CallExpr in pass.Files, resolves
// the callee to its (pkgPath, name) tuple via archtest.ResolvePackageRef, and
// flags hits whose owning package + name are in capForbiddenCtors — unless the
// file is the sanctioned provisioning site or a _test.go file.
func scanCapabilityProviderViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if rel == capWiringRel {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok {
				return
			}
			banned, hasPkg := capForbiddenCtors[pkgPath]
			if !hasPkg {
				return
			}
			if _, isBanned := banned[name]; !isBanned {
				return
			}
			line := p.Fset.Position(call.Pos()).Line
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"%s.%s is shared infrastructure and may only be constructed in %s; "+
						"cell modules must consume the injected capability.PGProvider / capability.RedisProvider",
					shortPkg(pkgPath), name, capWiringRel),
			})
		})
	}
	return out
}

// shortPkg returns the last path segment of an import path for messages.
func shortPkg(pkgPath string) string {
	if i := strings.LastIndex(pkgPath, "/"); i >= 0 {
		return pkgPath[i+1:]
	}
	return pkgPath
}

// TestCapabilityProviderFunnel_RedFixtureDetected asserts the production rule
// catches every banned call shape (qualified / aliased / dot) across both
// adapter packages in the RED fixture, gated by `//go:build archtest_fixture`.
//
// Coverage: 3 PG qualified (NewPool/NewTxManager/NewOutboxWriter) + 1 redis
// qualified (NewClient) + 1 aliased (NewPool) + 1 dot-import (NewTxManager) = 6.
func TestCapabilityProviderFunnel_RedFixtureDetected(t *testing.T) {
	diags := RunTypedFixture(t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/capfunnelfixture/..."},
		scanCapabilityProviderViolations,
	)
	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	// Equality (not ≥) so the fixture cannot drift silently — any change to the
	// fixture files must update this count.
	assert.Len(t, diags, 6,
		"fixture must yield exactly 6 CAPABILITY-PROVIDER-FUNNEL-01 hits "+
			"(3 PG qualified + 1 redis qualified + 1 aliased + 1 dot-import); "+
			"if the fixture changes intentionally, update the expected count")
}
