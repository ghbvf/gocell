// INVARIANT: REDIS-TEST-SEAM-CALLER-01
//
// Package archtest — single-rule file for REDIS-TEST-SEAM-CALLER-01.
//
// Rule: adapters/redis.NewClientForTest is a test-only construction seam that
// skips the connectivity Ping that NewClient performs (it exists so the
// composition-root session-cache wiring test in cellmodules/accesscore can
// exercise the happy path with a lazily-connected client — AUTH-CACHE-HAPPY-PATH-WIRING-TEST-01
// / #795). Production code MUST use NewClient. This rule forbids any caller of
// NewClientForTest outside a _test.go file.
//
// AI-robust grade: Medium (caller-allowlist; archtest type-aware via
// resolvePkgFuncCall → *types.Func pkg-path + name, so import aliases and
// dot-imports do not bypass it). The export is irreducible — the cross-package
// happy-path test (a different Go package than adapters/redis) cannot reach the
// unexported newClientFromCmdable, so a no-Ping seam must be exported. Hard
// alternatives (build-tag-gated symbol / unexported) are blocked by that
// cross-package test caller, so the caller-allowlist (_test.go only) is the
// practical ceiling — the same shape as AFTERCOMMIT-HOOK-PURE-TRANSIENT-01/A3.
//
// Blind spot: a production file forwarding the call through a value
// (fn := redis.NewClientForTest; fn(...)) is not resolved — the same
// forwarded-callee residual as other caller-allowlist rules. The ForTest name
// plus this rule make any direct production call a CI failure.

package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	redisTestSeamFn  = "NewClientForTest"
	redisPkgSuffixTS = "/adapters/redis"
)

// TestRedisTestSeamCaller_01 enforces REDIS-TEST-SEAM-CALLER-01: no non-_test.go
// production file may call adapters/redis.NewClientForTest.
func TestRedisTestSeamCaller_01(t *testing.T) {
	t.Parallel()
	var violations []string
	_ = Run(t, Production(TypedOpts{Tags: FlatNonDefaultTags()}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				pkgPath, name, ok := resolvePkgFuncCall(p.TypesInfo, call)
				if !ok || name != redisTestSeamFn || !strings.HasSuffix(pkgPath, redisPkgSuffixTS) {
					return
				}
				line := p.Fset.Position(call.Pos()).Line
				violations = append(violations, fmt.Sprintf(
					"%s:%d: REDIS-TEST-SEAM-CALLER-01: adapters/redis.NewClientForTest is a test-only seam "+
						"(skips connectivity Ping) — production code MUST use NewClient", rel, line))
			})
		}
		return nil
	})
	assert.Empty(t, violations,
		"REDIS-TEST-SEAM-CALLER-01: adapters/redis.NewClientForTest may only be called from _test.go files. "+
			"Production code must construct redis clients via NewClient (which validates config + verifies connectivity).")
}
