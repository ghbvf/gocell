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
// Blind spot (now reverse-self-checked): a production file forwarding the call
// through a value (fn := redis.NewClientForTest; fn(...)) is not caught by the
// direct-call scan above. Per ai-robust.md §"工具选定后强制盲区自检" this residual
// is closed by TestRedisTestSeamCaller_BlindSpot_ForwardedValue, which asserts
// NewClientForTest never appears in production AST as a value (any reference not
// in direct-callee position — bare ident or selector — fails), mirroring
// CACHING-SESSION-REVOKE's TestCachingSessionRevoke_BlindSpot_CacheMethodValue.
// The two checks compose: the primary rule bans the direct call, the blind-spot
// check bans the forwarded value, so no production reference to the seam survives.

package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
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

// TestRedisTestSeamCaller_BlindSpot_ForwardedValue closes the documented
// forwarded-value blind spot of REDIS-TEST-SEAM-CALLER-01: a production file
// could reference NewClientForTest as a VALUE (fn := redis.NewClientForTest;
// fn(...)) instead of calling it directly, evading the direct-call scan. This
// reverse self-check asserts NewClientForTest never appears in production AST in
// any non-callee position — bare ident or selector .Sel — resolved by go/types
// (TypesInfo.Uses) to the adapters/redis func, so aliases and dot-imports do not
// bypass it. FuncDecl definitions live in TypesInfo.Defs (not Uses) and are not
// flagged. Together with the primary direct-call rule, no production reference
// to the seam survives.
func TestRedisTestSeamCaller_BlindSpot_ForwardedValue(t *testing.T) {
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
			// First pass: collect the idents that sit in direct-callee position
			// (those are the primary rule's responsibility, not this check's).
			calleeIdents := make(map[*ast.Ident]bool)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					calleeIdents[fun] = true
				case *ast.SelectorExpr:
					calleeIdents[fun.Sel] = true
				}
			})
			// Second pass: any USE of NewClientForTest not in callee position is a
			// forwarded value.
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if id.Name != redisTestSeamFn || calleeIdents[id] {
					return
				}
				fn, _ := p.TypesInfo.Uses[id].(*types.Func)
				if fn == nil || fn.Pkg() == nil || !strings.HasSuffix(fn.Pkg().Path(), redisPkgSuffixTS) {
					return
				}
				line := p.Fset.Position(id.Pos()).Line
				violations = append(violations, fmt.Sprintf(
					"%s:%d: REDIS-TEST-SEAM-CALLER-01 blind-spot: adapters/redis.NewClientForTest referenced as a "+
						"value (forwarded function value) — a forward bypasses the direct-call rule", rel, line))
			})
		}
		return nil
	})
	assert.Empty(t, violations,
		"REDIS-TEST-SEAM-CALLER-01 blind-spot: adapters/redis.NewClientForTest must not be referenced as a value in "+
			"production (forwarding it through a func value evades the direct-call rule). Call NewClient instead.")
}
