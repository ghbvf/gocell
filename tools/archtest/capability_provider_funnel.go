// Importable rule body for CAPABILITY-PROVIDER-FUNNEL-01. Migrated from the
// legacy _test.go form to a non-test .go (M3 #1639) so the rule is
// module-path-agnostic — platform symbol paths are derived from
// [PlatformModulePath] (external.go), NOT bare "github.com/ghbvf/gocell…"
// literals (ARCHTEST-MODULE-PATH-FUNNEL-01). The dogfood + RED-fixture precision
// gate live in capability_provider_funnel_test.go.
//
// Not registered in StandardCellRules: the sole sanctioned provisioning site
// (cmd/corebundle/cap_wiring.go) and the banned shared-infra constructors are
// GoCell-internal composition-root layout; an external Cell repo has no such
// file, so the allowlist never matches and the rule degrades to a pure ban that
// would false-red any external module that legitimately constructs its own pool
// / client. Kept importable + module-path-agnostic but OUT of StandardCellRules
// (same disposition as the #1632 auth funnels).
//
// # CAPABILITY-PROVIDER-FUNNEL-01
//
// The shared-infrastructure constructors
//
//	adapters/postgres.NewPool / NewTxManager / NewOutboxWriter
//	adapters/redis.NewClient
//
// may only be invoked from the assembly's single provisioning site
// (cmd/corebundle/cap_wiring.go). Cell module files (cellmodules/<cell>/module.go)
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
// The sole sanctioned provisioning site (capWiringRel) is matched by
// isCapWiringSanctionedSite, which binds the exemption to PLATFORM PACKAGE
// IDENTITY (isGoCellPlatformPkgPath) — not a bare repo-relative path. This rule
// is importable: an external Cell repo can wire it via cfg.ExtraRules, and a
// consumer module could otherwise place its own cmd/corebundle/cap_wiring.go and
// call the banned constructors to claim the GoCell-internal exemption. A forged
// site has pkgPath = <consumer-module>/… (not under PlatformModulePath), so it is
// correctly never exempt and the rule stays a true pure ban there. Same
// provenance bind as isReconstructionAllowedSite (OUTBOX-RECONSTRUCTION-CALLER-01).
//
// # _test.go scope
//
// Run(t, Typed(TypedOpts{Tests: false}, ...)) loads only production-variant packages, so
// _test.go files are not in pass.Files; the scanner additionally filters by
// rel suffix as defense-in-depth — so the rule stays correct if a future
// caller passes Tests: true or runs it over a fixture (Run(t, Fixture(...)))
// load path where _test.go files would be present.
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
)

const (
	capFunnelRuleID = "CAPABILITY-PROVIDER-FUNNEL-01"
	// capPgImportPath / capRedisImportPath derive from PlatformModulePath so a
	// module rename / /v2 bump updates one place (ARCHTEST-MODULE-PATH-FUNNEL-01).
	capPgImportPath    = PlatformModulePath + "/adapters/postgres"
	capRedisImportPath = PlatformModulePath + "/adapters/redis"
	// capWiringRel is the sole sanctioned provisioning site, as a running-module
	// repo-relative path (NOT a platform module-path literal). It is consulted
	// only via isCapWiringSanctionedSite, which additionally requires the file's
	// package to be a GoCell platform package — so a consumer module that forges
	// this same relative path is NOT exempt (see isCapWiringSanctionedSite).
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

// CheckCapabilityProviderFunnel enforces CAPABILITY-PROVIDER-FUNNEL-01: the
// shared-infra adapter constructors may only be called from
// cmd/corebundle/cap_wiring.go. Cell module files must consume the injected
// capability.PGProvider / capability.RedisProvider. It scans the running
// module's cmd/ production packages and returns the diagnostics it observes;
// GoCell's TestCapabilityProviderFunnel_CompositionRootOnly calls it directly —
// single source, no parallel rule body.
func CheckCapabilityProviderFunnel(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Typed(
		TypedOpts{Tests: false},
		[]string{"./cmd/..."},
	), scanCapabilityProviderViolations)
}

// scanCapabilityProviderViolations walks every CallExpr in pass.Files, resolves
// the callee to its (pkgPath, name) tuple via archtest.ResolvePackageRef, and
// flags hits whose owning package + name are in capForbiddenCtors — unless the
// file is the sanctioned provisioning site or a _test.go file.
func scanCapabilityProviderViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	// pkgPath is constant across p.Files (a Pass is one loaded package); resolve
	// it once for the platform-identity bind in isCapWiringSanctionedSite.
	pkgPath := ""
	if p.Pkg != nil {
		pkgPath = p.Pkg.Path()
	}
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if isCapWiringSanctionedSite(pkgPath, rel) {
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
					shortPkg(pkgPath), name, capWiringRel,
				),
			})
		})
	}
	return out
}

// isCapWiringSanctionedSite reports whether the file at module-relative path rel
// in package pkgPath is the sole sanctioned shared-infra provisioning site. The
// exemption is bound to PLATFORM PACKAGE IDENTITY (isGoCellPlatformPkgPath), not
// a bare repo-relative path: this rule is importable and an external Cell repo
// wiring it via cfg.ExtraRules could otherwise place its own
// cmd/corebundle/cap_wiring.go, call the banned constructors, and claim the
// GoCell-internal exemption. A forged site in a consumer module has pkgPath =
// <consumer-module>/… (not under PlatformModulePath), so it is correctly never
// exempt and the rule stays a true pure ban there. In GoCell's own dogfood every
// production package is under PlatformModulePath, so the bind is transparent
// (rel→file is a bijection within a single module). Extracted as a pure function
// so the bind is unit-testable (TestIsCapWiringSanctionedSite). Same provenance
// bind as isReconstructionAllowedSite (OUTBOX-RECONSTRUCTION-CALLER-01).
func isCapWiringSanctionedSite(pkgPath, rel string) bool {
	return isGoCellPlatformPkgPath(pkgPath) && rel == capWiringRel
}

// shortPkg returns the last path segment of an import path for messages.
func shortPkg(pkgPath string) string {
	if i := strings.LastIndex(pkgPath, "/"); i >= 0 {
		return pkgPath[i+1:]
	}
	return pkgPath
}
