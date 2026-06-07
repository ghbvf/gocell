// INVARIANT: WEBHOOK-ALLOW-LOOPBACK-PROD-BAN-01
//
// WEBHOOK-ALLOW-LOOPBACK-PROD-BAN-01 — webhook.WithAllowLoopback is dev/CI-only.
//
// webhook.WithAllowLoopback() (kernel/webhook/ssrf.go, #1159 / KERNEL-WEBHOOK-01
// PR-4) is a SafeOption that exempts loopback (127.0.0.0/8, ::1) from the
// outbound SSRF blocklist — for testcontainers / local test servers only. Setting
// it in production would let loopback egress through with NO error. Before #1378
// this was a Soft godoc-only constraint ("must never be set in production"); this
// rule upgrades it to a Medium archtest.
//
//   - Rule (downstream Medium): scan ALL production (non-_test.go, non-generated)
//     Go files in the module for any reference to kernel/webhook.WithAllowLoopback.
//     Any such reference is a violation — the option may only appear in _test.go.
//     Detection: EachInSubtree[Ident] + info.Uses[id].(*types.Func).FullName() ==
//     "…/kernel/webhook.WithAllowLoopback". Resolving via go/types info.Uses
//     catches EVERY reference form — qualified (webhook.WithAllowLoopback),
//     aliased (kwh.WithAllowLoopback), AND dot-import (bare WithAllowLoopback) —
//     because all three resolve the reference ident to the same *types.Func; the
//     function *declaration* ident lives in info.Defs, so ssrf.go's definition
//     never false-fires. Today's inventory: 0 production references → vacuous green
//     (every caller is a kernel/webhook same-package test).
//
// AI-robust rating (Funnel 双向锁评级, per .claude/rules/gocell/ai-robust.md):
//
//	下游 Medium — archtest callsite ban, type-resolved (alias-safe). Form is a
//	  reference-presence scan, not a compile-time guarantee.
//	上游 Hard ACHIEVABLE but deferred → tracked gh #1730 (named here per charter
//	  §Funnel 双向锁评级: a Medium funnel whose Hard upgrade is achievable MUST
//	  track it in an issue and name the number here). All WithAllowLoopback callers
//	  today are kernel/webhook same-package tests, so unexporting it to
//	  `withAllowLoopback` would make any external (cmd/examples/runtime) reference a
//	  Go compile error = Hard upstream (mirrors the same-file `withResolver`
//	  test-only seam). #1378 deliberately keeps it EXPORTED to preserve the dev
//	  escape hatch and a path for future cross-package integration tests; #1730
//	  flips it to unexported (and retires this archtest) once that need is resolved.
//
// Blind spots (ai-robust 强制反向自检):
//
//	B-rule — rule-logic regression: testdata/webhook_allow_loopback_violate is a
//	  standalone-module production (.go) file that calls webhook.WithAllowLoopback;
//	  TestWebhookAllowLoopbackProdBan_ReverseFixture asserts the scan fires on it.
//	(No dot-import blind spot: the info.Uses Ident-walk resolves bare dot-import
//	  idents to the same *types.Func as qualified/aliased selectors, so all import
//	  forms are covered. The sibling WEBHOOK-SSRF-GUARD-01/A3 still uses a
//	  SelectorExpr-only scan with that documented gap; adopting this Ident-walk
//	  there is a separate, out-of-this-PR cleanup.)
//
// ref: docs/architecture/202605312300-1159-adr-webhook-ssrf-policy.md §Consequences
// ref: tools/archtest/webhook_ssrf_guard_test.go (production callsite-scan template)
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// webhookAllowLoopbackFunc is the banned exported function name in kernel/webhook.
const webhookAllowLoopbackFunc = "WithAllowLoopback"

// webhookAllowLoopbackFullName is the go/types FullName of the banned function.
// Matching info.Uses against this FullName catches every reference form
// (qualified / aliased / dot-import) because all resolve to the same *types.Func.
const webhookAllowLoopbackFullName = webhookPkgPath + "." + webhookAllowLoopbackFunc

// scanWebhookAllowLoopback implements the rule: any reference to
// kernel/webhook.WithAllowLoopback in a production file is a violation. It walks
// every identifier and matches info.Uses to the banned func's go/types FullName,
// so qualified/aliased/dot-import references are all caught; the func declaration
// (info.Defs, not info.Uses) is not flagged.
func scanWebhookAllowLoopback(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		fn, ok := info.Uses[id].(*types.Func)
		if !ok || fn.FullName() != webhookAllowLoopbackFullName {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(id.Pos()).Line,
			Message: "webhook.WithAllowLoopback referenced in production code; it is dev/CI-only " +
				"and must appear only in _test.go (WEBHOOK-ALLOW-LOOPBACK-PROD-BAN-01)",
		})
	})
	return out
}

// TestWebhookAllowLoopbackProdBan is the primary scan: no production
// (non-_test.go, non-generated) file in the module may reference
// kernel/webhook.WithAllowLoopback.
func TestWebhookAllowLoopbackProdBan(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		var out []Diagnostic
		for _, f := range p.Files {
			if p.IsGenerated(f) {
				continue
			}
			rel := p.Rel(f)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			out = append(out, scanWebhookAllowLoopback(p.Fset, f, rel, p.TypesInfo)...)
		}
		return out
	})

	Report(t, "WEBHOOK-ALLOW-LOOPBACK-PROD-BAN-01", diags)
}

// TestWebhookAllowLoopbackProdBan_ReverseFixture loads the synthetic violation
// fixture and asserts the scan fires — guards against the rule logic silently
// regressing to a vacuous pass (the production scan is vacuously green today
// because there are zero references).
func TestWebhookAllowLoopbackProdBan_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "webhook_allow_loopback_violate")

	var diags []Diagnostic
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanWebhookAllowLoopback(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	assert.GreaterOrEqual(t, len(diags), 1,
		"reverse fixture: expected ≥1 diagnostic for the production WithAllowLoopback callsite "+
			"in testdata/webhook_allow_loopback_violate — the rule must fire, else it is vacuous")
}
