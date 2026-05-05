// MESSAGE-CONST-LITERAL-01 — every call to `errcode.New(...)` and
// `errcode.Wrap(...)` in production code must pass a compile-time const
// literal as the third (`message`) argument. Runtime data (user input, IDs,
// counts, secrets) belongs in WithDetails (typed slog.Attr) or WithInternal
// (server-side only). The PII-safe default is enforced statically here so
// regression cannot reintroduce `fmt.Sprintf` / string-concatenation
// messages that leak runtime context onto the wire.
//
// Detection (AST + go/types):
//   1. Walk every production .go file (per fileroles.IsProductionCode).
//   2. Find every *ast.CallExpr whose Fun resolves via TypesInfo.Uses to
//      pkg/errcode.New or pkg/errcode.Wrap.
//   3. The third argument (index 2) must be either:
//        a) *ast.BasicLit with Kind == token.STRING — a string literal, or
//        b) *ast.Ident bound by TypesInfo.Uses to a *types.Const (untyped
//           string or string-typed package-level constant).
//      Any other shape — *ast.CallExpr (fmt.Sprintf, fmt.Errorf, etc.),
//      *ast.BinaryExpr (string concatenation), *ast.Ident bound to a
//      *types.Var (runtime variable) — is reported as a violation.
//
// Allow-list:
//   - pkg/errcode/ (the package defines the constructors and self-tests
//     them; Assertion's Sprintf-driven form is a deliberate exception
//     documented in the ctor's godoc).
//   - tools/archtest/testdata/ (fixture violations are intentional).
//   - _test.go files (handled implicitly by fileroles.IsProductionCode).
//
// Note: This rule does NOT constrain WithInternal arguments — runtime
// debugging context is the canonical home for fmt.Sprintf-formatted
// strings and never reaches the HTTP wire body.
//
// ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const ruleMessageConstLiteral01 = "MESSAGE-CONST-LITERAL-01"

// errcodeMessageAllowlist exempts pkg/errcode/ from the gate: the package
// defines New / Wrap and tests them with non-literal messages, and Assertion
// deliberately formats runtime context into Message (recorded in the
// constructor's godoc as a documented exception).
const errcodeMessageAllowlist = "pkg/errcode/"

// errcodeMessageTestdataAllowlist exempts archtest fixtures (where
// violations are intentional regression cases).
const errcodeMessageTestdataAllowlist = "tools/archtest/testdata/"

// errcodePackagePath is the canonical import path of the constructors
// targeted by this gate.
const errcodePackagePath = "github.com/ghbvf/gocell/pkg/errcode"

// httputilPackagePath / ctxcancelPackagePath are the additional helpers
// gated by this rule. Each helper accepts a caller-supplied message that
// flows directly into errcode.Error.Message; PR #391 review (P2) noted
// the prior carve-outs in their bodies (struct literal, no errcode.New
// involvement) created a static blind spot that this extension closes.
const httputilPackagePath = "github.com/ghbvf/gocell/pkg/httputil"
const ctxcancelPackagePath = "github.com/ghbvf/gocell/pkg/ctxcancel"

// gatedCallee describes one message-receiving entry point checked by the
// rule. messageArgIndex is the position of the message string in the
// argument list:
//   - errcode.New(kind, code, message, opts...)            → 2
//   - errcode.Wrap(kind, code, message, cause, opts...)    → 2
//   - httputil.WritePublic(ctx, w, kind, code, message)    → 4
//   - ctxcancel.WrapOrInfra(err, op, id, code, fallbackMsg) → 4
type gatedCallee struct {
	pkgPath         string
	name            string
	messageArgIndex int
	displayName     string // shown in violation messages, e.g. "httputil.WritePublic"
}

var messageGatedCallees = []gatedCallee{
	{pkgPath: errcodePackagePath, name: "New", messageArgIndex: 2, displayName: "errcode.New"},
	{pkgPath: errcodePackagePath, name: "Wrap", messageArgIndex: 2, displayName: "errcode.Wrap"},
	{pkgPath: httputilPackagePath, name: "WritePublic", messageArgIndex: 4, displayName: "httputil.WritePublic"},
	{pkgPath: ctxcancelPackagePath, name: "WrapOrInfra", messageArgIndex: 4, displayName: "ctxcancel.WrapOrInfra"},
}

// fixtureASTPackageNames maps the local-import name a fixture file uses
// (selector.X.Name) back to the canonical gatedCallee's displayName, for
// fixture-mode AST scanning where TypesInfo is unavailable. Each fixture
// imports the helper as the top-level package name.
var fixtureASTPackageNames = map[string]struct{}{
	"errcode":   {},
	"httputil":  {},
	"ctxcancel": {},
}

// TestErrcodeMessageConstLiteral enforces MESSAGE-CONST-LITERAL-01.
func TestErrcodeMessageConstLiteral(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackages(root, false, []string{"e2e", "integration", "pg"}, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			rel, ok := fileroles.Rel(root, abs)
			if !ok || !fileroles.IsProductionCode(rel) {
				continue
			}
			if strings.HasPrefix(rel, errcodeMessageAllowlist) {
				continue
			}
			if strings.HasPrefix(rel, errcodeMessageTestdataAllowlist) {
				continue
			}
			violations = append(violations, scanErrcodeMessageAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"%s: errcode.New/Wrap message must be a const literal — runtime data "+
			"belongs in WithDetails (slog.Attr) or WithInternal. "+
			"ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md",
		ruleMessageConstLiteral01)
}

// scanErrcodeMessageAST returns "<rel>:<line>: <kind>" violations for a
// single parsed file. info may be nil; in that case the resolver and
// isAcceptableMessageExpr fall back to pure-AST name matching (used by
// fixture scanning).
func scanErrcodeMessageAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee, ok := resolveGatedCallee(call, info)
		if !ok {
			return true
		}
		if len(call.Args) <= callee.messageArgIndex {
			return true
		}
		msgArg := call.Args[callee.messageArgIndex]
		if isAcceptableMessageExpr(msgArg, info) {
			return true
		}
		line := fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: %s(...) message must be a const literal (got %T) "+
				"— move runtime data to WithDetails(slog.Attr) or WithInternal",
			rel, line, callee.displayName, msgArg))
		return true
	})
	return out
}

// resolveGatedCallee matches call against messageGatedCallees and returns
// the matched gatedCallee. info-based resolution (production scan) checks
// the imported package path; AST-only fallback (fixture scan) checks the
// local selector name (e.g. selector.X.Name == "errcode") so fixtures can
// shadow the helper packages locally.
func resolveGatedCallee(call *ast.CallExpr, info *types.Info) (gatedCallee, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return gatedCallee{}, false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return gatedCallee{}, false
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return gatedCallee{}, false
		}
		pkgPath := fn.Pkg().Path()
		name := fn.Name()
		for _, c := range messageGatedCallees {
			if c.pkgPath == pkgPath && c.name == name {
				return c, true
			}
		}
		return gatedCallee{}, false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return gatedCallee{}, false
	}
	if _, registered := fixtureASTPackageNames[xIdent.Name]; !registered {
		return gatedCallee{}, false
	}
	for _, c := range messageGatedCallees {
		// AST-only mode keys on selector.X.Name == package short-name
		// (last segment of the import path). All four gated callees use
		// their natural short name in fixtures.
		shortName := lastPathSegment(c.pkgPath)
		if shortName == xIdent.Name && sel.Sel.Name == c.name {
			return c, true
		}
	}
	return gatedCallee{}, false
}

// lastPathSegment returns the substring after the final '/' in a Go import
// path — the package's natural short name when imported without an alias.
func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// isAcceptableMessageExpr reports whether expr is a const literal or a
// package-level string constant — the only forms allowed for the message
// argument under MESSAGE-CONST-LITERAL-01. info may be nil (fixture mode);
// in that case Ident / SelectorExpr fallbacks are accepted as const-like
// because the fixture cannot be type-checked, and the violations we care
// about are call-expression / binary-expression shapes that bypass the
// Ident branch entirely.
func isAcceptableMessageExpr(expr ast.Expr, info *types.Info) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.Ident:
		if info == nil {
			return true
		}
		obj := info.Uses[e]
		_, isConst := obj.(*types.Const)
		return isConst
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return false
		}
		if info == nil {
			return true
		}
		obj := info.Uses[e.Sel]
		_, isConst := obj.(*types.Const)
		return isConst
	default:
		return false
	}
}
