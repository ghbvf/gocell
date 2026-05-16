package archtest

// INVARIANT: SESSIONREFRESH-NO-SESSION-CREATE-01
//
// Refresh path must not mutate session.Store. session.ID is stable from
// login to logout (OAuth2 RFC 6749 §6 + OIDC Back-Channel Logout sid
// stability + ory/fosite Session.Clone + zitadel oidc_session aggregate +
// keycloak findOfflineUserSession). Refresh rotates the refresh-token chain
// and mints a new access JWT carrying the same sid claim — the session row
// is never created, revoked, or rotated from this path.
//
// 历史：commit fd954cb8 引入 "revoke-old + create-new session per refresh"
// 设计，调用 session.Store.Revoke(oldID) + session.Store.Create(newID) 来轮换
// session UUID。该设计偏离 OAuth2/OIDC 业界惯例且与 refresh chain 一致性
// 域冲突（child refresh row 仍继承旧 session_id，二次 refresh 失败）。在 PR
// #482 review 中撤回。本 archtest 保证未来 AI session 不会重新引入该模式。
//
// AI-rebust 评级：Medium (archtest type-aware) — type system 不强制
// session.Store.Create 在哪个 slice 不可调用，但 typeseval.ResolveMethodCall
// 让违反在 CI 时确定可见。Hard 形态需要把 session.Store 拆成 read-only +
// mutable 两个 sealed marker 由 composition root wrap，本 PR 范围外。
//
// 单条独立规则，按 ai-collab.md "{rule}_test.go" 命名。

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	sessionrefreshPkgPath    = "github.com/ghbvf/gocell/cells/accesscore/slices/sessionrefresh"
	sessionStorePkgPath      = "github.com/ghbvf/gocell/runtime/auth/session"
	sessionStoreInterfaceTyp = "Store"
)

// bannedSessionStoreMethods is the closed set of mutating method names on
// runtime/auth/session.Store. Refresh may call Get; everything that flips
// or appends state is banned in the refresh path.
var bannedSessionStoreMethods = map[string]struct{}{
	"Create":           {},
	"Revoke":           {},
	"RevokeForSubject": {},
}

// TestSessionrefreshNoSessionStoreMutation_01 fires when any file in
// cells/accesscore/slices/sessionrefresh (excluding _test.go) calls a banned
// method on session.Store. The rule resolves call targets through
// typeseval.ResolveMethodCall, so method-call (`s.Create(...)`), method-
// expression (`session.Store.Create(s, ...)`), and embedded-field promotion
// all collapse to the same *types.Func identity.
func TestSessionrefreshNoSessionStoreMutation_01(t *testing.T) {
	t.Parallel()

	var violations []string
	diags := RunTyped(t, TypedOpts{Tests: false}, []string{"./cells/accesscore/slices/sessionrefresh/..."}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		if p.Pkg.Path() != sessionrefreshPkgPath {
			// Sibling subpackages (currently none) are out of scope; this
			// keeps the rule's blast radius pinned to the slice's own code.
			return nil
		}
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			ds = append(ds, scanSessionrefreshFile(p, file, rel)...)
		}
		return ds
	})

	for _, d := range diags {
		violations = append(violations, d.Message)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Logf("%s", v)
	}
	const failMsg = "rule SESSIONREFRESH-NO-SESSION-CREATE-01: refresh path " +
		"must not call mutating methods on session.Store (Create / Revoke / " +
		"RevokeForSubject). session.ID is stable from login to logout " +
		"(OAuth2 RFC 6749 §6 + OIDC Back-Channel Logout sid stability + " +
		"ory/fosite / zitadel / keycloak alignment). Cross-store mutation " +
		"in refresh is a recurrence of the design defect fixed by PR #482 " +
		"review (commit fd954cb8 撤回)"
	assert.Empty(t, violations, failMsg)
}

// scanSessionrefreshFile walks file's AST for CallExpr nodes whose method
// receiver resolves to runtime/auth/session.Store and whose method name is
// in bannedSessionStoreMethods. EachInSubtree[ast.CallExpr] traverses the
// full file tree — nested function literals and closures are covered.
func scanSessionrefreshFile(
	p *Pass,
	file *ast.File,
	rel string,
) []Diagnostic {
	var ds []Diagnostic

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		methodName := sel.Sel.Name
		if _, banned := bannedSessionStoreMethods[methodName]; !banned {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok {
			return
		}
		// Filter by owning package = runtime/auth/session and that the
		// receiver interface is named Store. Receiver inspection guards
		// against shadowing the method name on an unrelated type.
		if fn.Pkg() == nil || fn.Pkg().Path() != sessionStorePkgPath {
			return
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Recv() == nil {
			return
		}
		named, ok := receiverNamedType(sig.Recv().Type())
		if !ok || named.Obj().Name() != sessionStoreInterfaceTyp {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		ds = append(ds, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"%s:%d: SESSIONREFRESH-NO-SESSION-CREATE-01: forbidden session.Store.%s call from refresh path",
				rel, line, methodName),
		})
	})

	return ds
}

// receiverNamedType unwraps pointer / alias layers to recover the *types.Named
// the method is attached to. Method receivers on session.Store (an interface)
// are interface-named, so the *types.Named lookup is straightforward.
func receiverNamedType(t types.Type) (*types.Named, bool) {
	switch v := t.(type) {
	case *types.Pointer:
		return receiverNamedType(v.Elem())
	case *types.Named:
		return v, true
	}
	return nil, false
}
