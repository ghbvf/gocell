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
// single parsed file. info may be nil; in that case errcodeConstructorName
// and isAcceptableMessageExpr fall back to pure-AST name matching (used by
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
		ctorName, ok := errcodeConstructorName(call, info)
		if !ok {
			return true
		}
		// New: (kind, code, message, opts...)        — message is index 2
		// Wrap: (kind, code, message, cause, opts...) — message is index 2
		if len(call.Args) < 3 {
			return true
		}
		msgArg := call.Args[2]
		if isAcceptableMessageExpr(msgArg, info) {
			return true
		}
		line := fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: errcode.%s(...) message must be a const literal (got %T) "+
				"— move runtime data to WithDetails(slog.Attr) or WithInternal",
			rel, line, ctorName, msgArg))
		return true
	})
	return out
}

// errcodeConstructorName resolves call's Fun to either errcode.New or
// errcode.Wrap and returns the function name; returns "" / false otherwise.
//
// Resolution path:
//   - If TypesInfo is available, the gate matches via *types.Func -> *types.Package
//     path equality with errcodePackagePath. This is the precise mode used by
//     the main test (real production sources, real imports).
//   - If TypesInfo is nil (fixture-mode AST scan), the gate falls back to
//     pure-AST name matching: selector.X.Name == "errcode" and
//     selector.Sel.Name in {"New", "Wrap"}. This lets fixture packages
//     declare their own local `errcode` package without a replace directive
//     pointing at the main module.
func errcodeConstructorName(call *ast.CallExpr, info *types.Info) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return "", false
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return "", false
		}
		pkg := fn.Pkg()
		if pkg == nil || pkg.Path() != errcodePackagePath {
			return "", false
		}
		switch fn.Name() {
		case "New", "Wrap":
			return fn.Name(), true
		default:
			return "", false
		}
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok || xIdent.Name != "errcode" {
		return "", false
	}
	if sel.Sel == nil {
		return "", false
	}
	switch sel.Sel.Name {
	case "New", "Wrap":
		return sel.Sel.Name, true
	default:
		return "", false
	}
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
