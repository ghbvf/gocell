// INVARIANT: MQTT-CONNECT-DEADLINE-DECOUPLED-01
//
// adapters/mqtt.Open MUST keep two contexts decoupled:
//
//  1. autopaho.NewConnection(ctx, …) receives Open's lifecycle ctx param —
//     the ConnectionManager retries/reconnects bound to it (autopaho godoc:
//     "will retry until the context is cancelled").
//  2. (*Connection).waitFirstConnection(connectCtx) receives a SEPARATE
//     context derived from context.WithTimeout/WithDeadline — the bounded
//     bootstrap first-connection wait.
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

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
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
	// context.WithTimeout / context.WithDeadline inside Open's body.
	deadlineObjs := collectDeadlineCtxObjects(openFD.Body, info)

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
			"(that is the #1388 collapse); pass a context.WithTimeout(ctx, cfg.ConnectDeadline) child", ruleID)
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
// context.WithTimeout / context.WithDeadline call inside body. Both return
// (ctx, cancel), so the assignment form `cctx, cancel := context.WithTimeout(…)`
// is the only compilable shape; the LHS[0] object is the derived ctx.
func collectDeadlineCtxObjects(body *ast.BlockStmt, info *types.Info) map[types.Object]struct{} {
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
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			if obj := info.ObjectOf(id); obj != nil {
				out[obj] = struct{}{}
			}
		}
	})
	return out
}
