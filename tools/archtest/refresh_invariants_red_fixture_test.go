// invariants:
//   - INVARIANT: REFRESH-CROSS-STORE-TX-01 (RED fixture coverage)
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestRefreshCrossStoreTX01_RedFixtureDetected asserts the rule catches both
// banned call shapes in the fixture at
// tools/archtest/internal/refreshinvariantsfixture/refresh_red.go (gated by
// `//go:build archtest_fixture`):
//
//   - s.refreshStore.Peek(...)    outside RunInTx closure → caught by Soft + Medium
//   - s.sessionStore.Get(...)     outside RunInTx closure → caught only by Medium
//
// # T3 Wave 1 (RED — Soft rule predicate)
//
// The current production rule's guardedCalls map contains stale entries
// `sessionRepo.Update` / `sessionRepo.GetByID` (cells/accesscore/slices/
// sessionrefresh/service.go switched from cell-private SessionRepository to
// runtime/auth/session.Store in PR #482; the rule was not updated). The new
// lookup chain is Peek → sessionStore.Get → userRepo.GetByID → Rotate, but
// `sessionStore.Get` is not in the map. Soft catches refreshStore.Peek only;
// it misses sessionStore.Get. This test asserts ≥ 2 hits and is therefore
// RED at Wave 1, GREEN at Wave 2 (guardedCalls updated + ResolveMethodCall
// receiver-type validation).
//
// Reuses the Wave-1 (Soft) predicate inline. Wave 2 rewrites this test to
// share the production scan helper.
func TestRefreshCrossStoreTX01_RedFixtureDetected(t *testing.T) {
	root := findModuleRoot(t)
	fixturePath := filepath.Join(root, "tools", "archtest", "internal",
		"refreshinvariantsfixture", "refresh_red.go")

	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, fixturePath, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	// Locate the Refresh method on Service.
	var refresh *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](af, func(fn *ast.FuncDecl) {
		if refresh != nil || fn.Recv == nil || fn.Name.Name != "Refresh" {
			return
		}
		refresh = fn
	})
	require.NotNil(t, refresh, "fixture must define Service.Refresh: %s", fixturePath)

	// Find the single s.txRunner.RunInTx call + capture its closure range.
	var runInTxClosure *ast.FuncLit
	scanner.EachInSubtree[ast.CallExpr](refresh.Body, func(call *ast.CallExpr) {
		if !isTxRunnerRunInTxCall(call) || runInTxClosure != nil {
			return
		}
		if len(call.Args) < 2 {
			return
		}
		fl, ok := call.Args[1].(*ast.FuncLit)
		if ok {
			runInTxClosure = fl
		}
	})
	require.NotNil(t, runInTxClosure,
		"fixture's Refresh must call s.txRunner.RunInTx with an inline closure")

	// Wave 1 (Soft) guarded set — production rule body lines 139-146. Note
	// the absence of "sessionStore.Get" — the gap this RED test exposes.
	guarded := map[string]bool{
		"refreshStore.Peek":          true,
		"refreshStore.Rotate":        true,
		"refreshStore.RevokeSession": true,
		"sessionRepo.Update":         true, // stale post-PR #482
		"sessionRepo.GetByID":        true, // stale post-PR #482
		"userRepo.GetByID":           true,
	}

	lbrace := runInTxClosure.Body.Lbrace
	rbrace := runInTxClosure.Body.Rbrace

	type violation struct {
		line int
		name string
	}
	var hits []violation
	scanner.EachInSubtree[ast.CallExpr](refresh.Body, func(call *ast.CallExpr) {
		// Skip calls inside the closure (desired location).
		if call.Pos() > lbrace && call.Pos() < rbrace {
			return
		}
		name, ok := bareServiceFieldCall(call)
		if !ok {
			return
		}
		if !guarded[name] {
			return
		}
		hits = append(hits, violation{
			line: fset.Position(call.Pos()).Line,
			name: name,
		})
	})

	for _, h := range hits {
		t.Logf("RED fixture hit: line %d s.%s outside RunInTx closure", h.line, h.name)
	}

	// Fixture has 2 violations outside the closure:
	//   1. s.refreshStore.Peek  → caught by Soft + Medium
	//   2. s.sessionStore.Get   → caught only by Medium (after guardedCalls update)
	require.GreaterOrEqual(t, len(hits), 2,
		fmt.Sprintf("RED fixture: expected ≥ 2 violations (refreshStore.Peek + sessionStore.Get), got %d. "+
			"Soft guardedCalls map is stale (sessionRepo.* deleted in PR #482; sessionStore.Get not added). "+
			"This test stays RED until T3 Wave 2 updates the map and adds typeseval.ResolveMethodCall.", len(hits)))

	// Stronger assertion: must specifically catch sessionStore.Get.
	var sawSessionStoreGet bool
	for _, h := range hits {
		if h.name == "sessionStore.Get" {
			sawSessionStoreGet = true
			break
		}
	}
	assert.True(t, sawSessionStoreGet,
		"RED fixture: rule MUST flag s.sessionStore.Get outside RunInTx closure; "+
			"Soft predicate omits it from guardedCalls (post-PR #482 stale state)")
}
