package archtest

// session_revoked_field_access_test.go — independent Hard funnel that locks
// session.{Session,ValidateView}.RevokedAt field access to an enumerable
// owner-package allowlist. Replaces the within-credentialauthority sub-funnel
// (SessionNotRevoked Check) that conflated session-state with user-bound
// credential checks (see ADR §A11 rewrite + §A12 wire-uniformity).
//
// INVARIANT: SESSION-REVOKED-FIELD-ACCESS-01
//
// Hard funnel rating (ai-collab.md §"AI-rebust 三档分级"):
//
//   Upstream Hard (field-access allowlist):
//     Every SelectorExpr that reads session.Session.RevokedAt or
//     session.ValidateView.RevokedAt resolves via *types.Info.Selections to
//     a specific *types.Var (field) identity. The current production module
//     enumerates a fixed allowlist of import paths that may read the field;
//     any read from outside that allowlist fails archtest in CI.
//     Enumeration form (typed package import path) matches the funnel-scope
//     convention used by CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 — names are
//     not string-anchored, alias / dot-import / shadowed selector cannot
//     bypass typed resolution.
//
// Wire-uniformity防枚举（ADR §A12）：拒绝 revoked session 时 wire 层必须返回与
// "user-not-found / userRepo unavailable / inactive user" 同一个 errcode
// envelope。SESSION-REVOKED-FIELD-ACCESS-01 锁字段访问点，wire-uniformity 由
// per-slice service_test.go 通过结构化字段断言 + revoked + repoErr / revoked
// + inactive 组合用例守护（trigger backlog: WIRE-UNIFORM-RESPONSE-ARCHTEST-01）。
//
// Scanning tools:
//   - *types.Info.Selections lookup over EachInSubtree[ast.SelectorExpr]
//
// Blind-spot self-checks (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. reflect.Value.FieldByName("RevokedAt"): bypasses SelectorExpr
//     resolution; the field name is in a string literal. Captured by:
//     TestSessionRevokedFieldAccess_BlindSpot_ReflectFieldByName.
//
//  2. unsafe.Pointer offset read of Session / ValidateView.RevokedAt:
//     bypasses Go field visibility entirely. Captured by:
//     TestSessionRevokedFieldAccess_BlindSpot_UnsafePointerImport
//     (rejects unsafe import in scope where the field is reachable).
//
// Known caveats (archtest CANNOT close these; documented for review):
//   a. Cross-package helper wrappers that read RevokedAt internally and
//      expose a boolean — the AST scan only sees the helper's own read,
//      flagging the helper's owning package. If a new helper package is
//      added, registering it in the allowlist is a deliberate review act.
//   b. Reading the field via an interface abstraction over ValidateView /
//      Session. Production code holds concrete *session.ValidateView; if
//      that changes, the scan must be extended to interface-origin lookup
//      (same shape as CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 caveat).
//
// RED fixture: testdata/session_revoked_field_red simulates an outside
// reader. Self-check assertion: the fixture must produce ≥1 violation.

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// revokedAtAllowlist enumerates the production packages that may read
// session.{Session,ValidateView}.RevokedAt. Order is package-tree depth
// (slices then runtime); _test.go files always pass.
//
// Adding a new entry is a deliberate review act — the entry must be ADR-
// referenced (§A11 / §A12) and the reader must justify why the read cannot
// be expressed as a wire-uniform comparison inside the listed packages.
var revokedAtAllowlist = []string{
	"cells/accesscore/slices/sessionvalidate/",
	"cells/accesscore/slices/sessionrefresh/",
	"runtime/auth/session/", // field-defining package; internal session model logic
}

// isRevokedAtReaderAllowlisted reports whether rel may read RevokedAt.
// Test files always pass.
func isRevokedAtReaderAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range revokedAtAllowlist {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// TestSessionRevokedFieldAccess_Upstream_01 enforces the field-access
// allowlist. Any production read of session.{Session,ValidateView}.RevokedAt
// from outside the allowlist is a violation.
//
// RED fixture: testdata/session_revoked_field_red.
func TestSessionRevokedFieldAccess_Upstream_01(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/...",
		"./runtime/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isRevokedAtReaderAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanRevokedAtReads(p, file, rel)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"SESSION-REVOKED-FIELD-ACCESS-01: production reads of "+
			"session.{Session,ValidateView}.RevokedAt outside the owner-package "+
			"allowlist (sessionvalidate/, sessionrefresh/, runtime/auth/session/). "+
			"Adding a new reader requires updating revokedAtAllowlist + ADR §A11/§A12.")

	verifyRevokedAtReadRedFixtureDetected(
		t,
		"./cells/accesscore/internal/credentialauthority/testdata/session_revoked_field_red",
		"SESSION-REVOKED-FIELD-ACCESS-01 RED fixture",
	)
}

// scanRevokedAtReads flags SelectorExpr reads of session.Session.RevokedAt or
// session.ValidateView.RevokedAt resolved via *types.Info.
func scanRevokedAtReads(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != credRevokedAt {
			return
		}
		selection := p.TypesInfo.Selections[sel]
		if selection == nil {
			return
		}
		obj := selection.Obj()
		field, ok := obj.(*types.Var)
		if !ok || !field.IsField() {
			return
		}
		recv := selection.Recv()
		if recv == nil {
			return
		}
		owner := typeOwner(recv)
		if owner == nil {
			return
		}
		ownerPkg := owner.Pkg()
		if ownerPkg == nil || ownerPkg.Path() != credSessionPkgPath {
			return
		}
		if owner.Name() != credSessionType && owner.Name() != credSessionViewType {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: SESSION-REVOKED-FIELD-ACCESS-01: read of %s.%s.RevokedAt "+
				"outside the owner-package allowlist",
			rel, line, ownerPkg.Path(), owner.Name(),
		))
	})
	return out
}

func verifyRevokedAtReadRedFixtureDetected(t *testing.T, pattern, label string) {
	t.Helper()
	var found int
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanRevokedAtReads(p, file, label))
		}
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture reads session.{Session,ValidateView}.RevokedAt "+
			"and is type-checkable.",
		label)
}

// TestSessionRevokedFieldAccess_BlindSpot_ReflectFieldByName asserts that
// reflect.Value.FieldByName("RevokedAt") does NOT appear in production code
// under cells/ + runtime/ + cmd/, confirming the reflect blind spot is not
// exercised. (CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01's reflect blind-spot
// test scopes to cells/accesscore + cmd; this one extends the same check
// to runtime/auth/session so the field-defining package is also covered.)
func TestSessionRevokedFieldAccess_BlindSpot_ReflectFieldByName(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/...",
		"./runtime/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "FieldByName" {
					return
				}
				if len(call.Args) != 1 {
					return
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return
				}
				name := strings.Trim(lit.Value, `"`)
				if name == credRevokedAt {
					line := p.Fset.Position(call.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: reflect.FieldByName(%q) blind spot detected — "+
							"archtest cannot see reflect-based field reads",
						rel, line, name,
					))
				}
			})
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"SESSION-REVOKED-FIELD-ACCESS-01 blind-spot: reflect.FieldByName of "+
			"RevokedAt found in production code — archtest cannot see "+
			"reflect-based field reads.")
}

// TestSessionRevokedFieldAccess_BlindSpot_UnsafePointerImport asserts that
// no production file under cells/ + cmd/ imports "unsafe", which would
// permit offset-based reads of Session / ValidateView fields bypassing the
// Go field-access machinery. runtime/auth/session/ legitimately defines
// the struct; adapters/postgres/ legitimately needs unsafe for pgx but is
// out of scope here.
func TestSessionRevokedFieldAccess_BlindSpot_UnsafePointerImport(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			for _, imp := range file.Imports {
				if imp.Path == nil {
					continue
				}
				impPath := strings.Trim(imp.Path.Value, `"`)
				if impPath == "unsafe" {
					line := p.Fset.Position(imp.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: imports \"unsafe\" — potential offset read of "+
							"session.{Session,ValidateView}.RevokedAt could bypass "+
							"SESSION-REVOKED-FIELD-ACCESS-01",
						rel, line,
					))
				}
			}
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"SESSION-REVOKED-FIELD-ACCESS-01 blind-spot: unsafe import found in "+
			"cells/ or cmd/ — verify no unsafe.Pointer reads target session "+
			"protocol-protected fields.")
}
