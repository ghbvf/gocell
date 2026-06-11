//go:build archtest

// INVARIANT: MQTT-CONNECT-DEADLINE-DECOUPLED-01
//
// adapters/mqtt.Open MUST keep two contexts decoupled:
//
//  1. autopaho.NewConnection(ctx, …) receives Open's lifecycle ctx param —
//     the ConnectionManager retries/reconnects bound to it (autopaho godoc:
//     "will retry until the context is canceled").
//  2. (*Connection).waitFirstConnection(connectCtx) receives a SEPARATE
//     context derived from context.WithTimeout(ctx, cfg.connectDeadline) — the
//     bounded bootstrap first-connection wait. Both semantic args are locked
//     (base = the lifecycle ctx param, budget = the Config.connectDeadline
//     field), so a decoy like WithTimeout(context.Background(), 1*time.Hour)
//     does NOT satisfy A2 — the guard binds the deadline to its config source,
//     not merely to "some timeout context".
//
// Collapsing the two (passing the same ctx to both, as Open did before #1388)
// makes the first-connection wait inherit the lifecycle ctx's lifetime; when
// that ctx is an effectively-unbounded root/app ctx the bootstrap hangs
// forever (PR #1364 review F4, P1·Cx3). This archtest is the regression guard.
//
// AI-robust rating: Medium — and this IS the Hard ceiling for this symbol.
// waitFirstConnection is an unexported method whose only caller is the
// same-package Open. A typed-wrapper "sealed construction" downstream-Hard
// would only guard an external caller that does not exist; the real
// regression risk is 100% in-package, and Go cannot express "which in-package
// callsite may pass which value". Same permanent Medium ceiling as
// SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893). No upgrade
// issue is opened because no lower-cost Hard form exists.
//
// Scanner blind spots (per ai-robust.md §"工具选定后强制盲区自检"), each with
// a reverse self-check below:
//   - Side-effect relocation: if a future refactor moves NewConnection or
//     waitFirstConnection out of Open's direct body (e.g. into a helper), the
//     scanner would vacuously pass. Guarded by requiring ≥1 of each call in
//     Open's body (else fail "side effect moved, update rule").
//   - connectCtx derivation extracted to a helper: deadline-source objects are
//     collected only from context.WithTimeout/WithDeadline assignments INSIDE
//     Open. A helper-derived connectCtx would not be in that set, so A2 fails
//     closed and forces a rule update — the intended fail-closed behavior.
//
// ref: adapters/mqtt.Open; eclipse/paho.golang autopaho/auto.go AwaitConnection
// (the upstream separation this rule restores).
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMQTTConnectDeadlineDecoupled01 enforces MQTT-CONNECT-DEADLINE-DECOUPLED-01.
func TestMQTTConnectDeadlineDecoupled01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-CONNECT-DEADLINE-DECOUPLED-01"

	var openFD *ast.FuncDecl
	var info *types.Info

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Name != nil && fd.Name.Name == "Open" && fd.Recv == nil {
						openFD = fd
						info = p.TypesInfo
					}
				})
			}
			return nil
		})

	require.NotNil(t, openFD, "%s: cannot find FuncDecl Open inside adapters/mqtt — rule must be updated", ruleID)
	require.NotNil(t, openFD.Body, "%s: Open has no body", ruleID)
	require.NotNil(t, info, "%s: TypesInfo unavailable — go/types loading failed", ruleID)

	ctxObj := openCtxParamObject(t, openFD, info, ruleID)

	// A1: autopaho.NewConnection(arg0, …) — arg0 must be the lifecycle ctx param.
	newConnCall, foundNewConn := FindFirstInSubtree[ast.CallExpr](openFD.Body, func(call *ast.CallExpr) bool {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		return ok && name == "NewConnection" && pkgPath == "github.com/eclipse/paho.golang/autopaho"
	})
	require.True(t, foundNewConn,
		"%s: Open body must contain an autopaho.NewConnection call (otherwise the "+
			"CM-lifecycle binding moved and this rule needs updating)", ruleID)
	require.NotEmpty(t, newConnCall.Args, "%s: autopaho.NewConnection called with no args", ruleID)
	if id, ok := newConnCall.Args[0].(*ast.Ident); ok {
		assert.Equal(t, ctxObj, info.ObjectOf(id),
			"%s: A1 — autopaho.NewConnection must receive Open's lifecycle ctx param "+
				"(the ConnectionManager binds to it), got %q", ruleID, id.Name)
	} else {
		t.Errorf("%s: A1 — autopaho.NewConnection first arg must be the ctx ident, got %T",
			ruleID, newConnCall.Args[0])
	}

	// Collect connect-deadline-source objects: vars assigned from
	// context.WithTimeout(ctx, cfg.connectDeadline) inside Open's body — both
	// semantic args locked (base = lifecycle ctx param, budget = connectDeadline
	// field), so a `WithTimeout(context.Background(), 1*time.Hour)` decoy does not
	// qualify.
	deadlineObjs := collectDeadlineCtxObjects(openFD.Body, info, ctxObj)

	// A2: (*Connection).waitFirstConnection(arg0) — arg0 must be a deadline-derived
	// ctx, NOT the lifecycle ctx param.
	waitCall, foundWait := FindFirstInSubtree[ast.CallExpr](openFD.Body, func(call *ast.CallExpr) bool {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel != nil && sel.Sel.Name == "waitFirstConnection"
	})
	require.True(t, foundWait,
		"%s: Open body must contain a c.waitFirstConnection call (otherwise the "+
			"bootstrap-wait boundary moved and this rule needs updating)", ruleID)
	require.NotEmpty(t, waitCall.Args, "%s: waitFirstConnection called with no args", ruleID)

	arg0, ok := waitCall.Args[0].(*ast.Ident)
	require.True(t, ok,
		"%s: A2 — waitFirstConnection first arg must be a ctx ident, got %T", ruleID, waitCall.Args[0])
	arg0Obj := info.ObjectOf(arg0)
	assert.NotEqual(t, ctxObj, arg0Obj,
		"%s: A2 — waitFirstConnection must NOT receive the lifecycle ctx param "+
			"(that is the #1388 collapse); pass a context.WithTimeout(ctx, cfg.connectDeadline) child", ruleID)
	assert.Contains(t, deadlineObjs, arg0Obj,
		"%s: A2 — waitFirstConnection arg %q must be derived from context.WithTimeout/WithDeadline "+
			"inside Open", ruleID, arg0.Name)
}

// openCtxParamObject returns the types.Object of Open's first (ctx) parameter.
func openCtxParamObject(t *testing.T, fd *ast.FuncDecl, info *types.Info, ruleID string) types.Object {
	t.Helper()
	require.NotNil(t, fd.Type, "%s: Open has no type", ruleID)
	require.NotNil(t, fd.Type.Params, "%s: Open has no params", ruleID)
	require.NotEmpty(t, fd.Type.Params.List, "%s: Open has no params", ruleID)
	names := fd.Type.Params.List[0].Names
	require.NotEmpty(t, names, "%s: Open first param is unnamed", ruleID)
	obj := info.ObjectOf(names[0])
	require.NotNil(t, obj, "%s: cannot resolve Open ctx param object", ruleID)
	return obj
}

// collectDeadlineCtxObjects returns the set of var objects assigned from a
// context.WithTimeout(ctxObj, cfg.connectDeadline) call inside body. Both
// semantic args are locked, NOT just the WithTimeout form:
//   - arg 0 (base ctx) MUST resolve to ctxObj (Open's lifecycle ctx param) — a
//     fresh context.Background()/TODO() base would decouple the bootstrap wait
//     from the lifecycle ctx in the wrong direction and is rejected.
//   - arg 1 (budget) MUST be the mqtt.Config.connectDeadline field selector — an
//     arbitrary literal (e.g. 1*time.Hour) or a different duration field is
//     rejected, so the regression guard genuinely binds the deadline source.
//
// WithTimeout returns (ctx, cancel), so the assignment form
// `cctx, cancel := context.WithTimeout(…)` is the only compilable shape; the
// LHS[0] object is the derived ctx. (WithDeadline takes a time.Time, not the
// connectDeadline duration, so it cannot satisfy the budget check — only
// WithTimeout qualifies in practice.)
func collectDeadlineCtxObjects(body *ast.BlockStmt, info *types.Info, ctxObj types.Object) map[types.Object]struct{} {
	out := map[types.Object]struct{}{}
	EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		if len(as.Rhs) != 1 || len(as.Lhs) == 0 {
			return
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != "context" || (name != "WithTimeout" && name != "WithDeadline") {
			return
		}
		if len(call.Args) != 2 {
			return
		}
		// arg 0: base ctx must be Open's lifecycle ctx param.
		baseIdent, ok := call.Args[0].(*ast.Ident)
		if !ok || info.ObjectOf(baseIdent) != ctxObj {
			return
		}
		// arg 1: budget must be the mqtt.Config.connectDeadline field.
		if !isConnectDeadlineField(call.Args[1], info) {
			return
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			if obj := info.ObjectOf(id); obj != nil {
				out[obj] = struct{}{}
			}
		}
	})
	return out
}

// isConnectDeadlineField reports whether expr is a selector resolving to the
// connectDeadline field of mqtt.Config (e.g. `cfg.connectDeadline`).
func isConnectDeadlineField(expr ast.Expr, info *types.Info) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "connectDeadline" {
		return false
	}
	selection, ok := info.Selections[sel]
	if !ok || selection.Kind() != types.FieldVal {
		return false
	}
	recv := selection.Recv()
	if ptr, isPtr := recv.(*types.Pointer); isPtr {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == mqttPkgPath &&
		named.Obj().Name() == "Config"
}
