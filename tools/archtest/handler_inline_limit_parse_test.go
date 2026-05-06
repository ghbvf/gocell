package archtest

// handler_inline_limit_parse_test.go — HANDLER-NO-INLINE-LIMIT-PARSE-01.
//
// PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE F4 absorbs PR#376 F-COR-001: every
// paginated endpoint must route the cursor+limit pair through
// pkg/httputil.ParsePageParams so the limit error envelope is uniform across
// the entire HTTP surface. This rule statically guards the generator against
// regressing to per-param inline limit parsing — any generated handler that
// emits a strconv.ParseInt call alongside a "limit" string literal in the
// same function body is flagged as a codegen drift.
//
// The check is intentionally narrow: it only inspects the generated
// handler_gen.go files (cells/* and examples/* hand-written code may legitimately
// parse their own limit query params). Generated handlers should always go
// through ParsePageParams when the contract.yaml declares cursor+limit, no
// matter how many additional filter params are present (the F4 relaxation in
// builder.detectPagination + handler.tmpl pagination branch).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const handlerInlineLimitParseRule = "HANDLER-NO-INLINE-LIMIT-PARSE-01"

// inlineLimitParseViolation is one (file, function, line) coordinate where
// generated handler code parses a "limit" query param via strconv.ParseInt
// instead of httputil.ParsePageParams.
type inlineLimitParseViolation struct {
	File string
	Func string
	Line int
}

func TestHandlerNoInlineLimitParse(t *testing.T) {
	root := findModuleRoot(t)

	handlers, err := filepath.Glob(filepath.Join(root, "generated", "contracts", "http", "**", "handler_gen.go"))
	require.NoError(t, err)
	// Recursive glob fallback: stdlib filepath.Glob does not expand ** by
	// default. Walk the tree manually if Glob returned 0 entries.
	if len(handlers) == 0 {
		handlers = walkHandlerGenFiles(t, filepath.Join(root, "generated", "contracts", "http"))
	}
	require.NotEmpty(t, handlers, "no generated handler_gen.go files found under generated/contracts/http")

	var violations []inlineLimitParseViolation
	for _, path := range handlers {
		violations = append(violations, scanInlineLimitParse(t, path)...)
	}

	if !assert.Empty(t, violations, handlerInlineLimitParseRule+": generated handlers must route limit through httputil.ParsePageParams") {
		for _, v := range violations {
			t.Logf("%s: %s:%d in func %s", handlerInlineLimitParseRule, v.File, v.Line, v.Func)
		}
	}
}

// walkHandlerGenFiles collects every handler_gen.go under base, recursive.
// Used when filepath.Glob's ** glob is not supported by the platform/version.
func walkHandlerGenFiles(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && d.Name() == "handler_gen.go" {
			out = append(out, path)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

// scanInlineLimitParse parses a single handler_gen.go and returns one
// violation per top-level function whose body contains both a strconv.ParseInt
// call and a string literal "limit". The two-condition match keeps the rule
// from flagging legitimate generic int64 query param parsing (a body that
// contains strconv.ParseInt for some unrelated param "page" is fine).
func scanInlineLimitParse(t *testing.T, path string) []inlineLimitParseViolation {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Logf("scan %s: parse error %v (skipping)", path, err)
		return nil
	}

	var out []inlineLimitParseViolation
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		hasParseInt := false
		hasLimitLiteral := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				// strconv.ParseInt — receiver is package selector.
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "strconv" && x.Sel.Name == "ParseInt" {
					hasParseInt = true
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING && (x.Value == `"limit"` || strings.EqualFold(x.Value, `"limit"`)) {
					hasLimitLiteral = true
				}
			}
			return true
		})
		if hasParseInt && hasLimitLiteral {
			out = append(out, inlineLimitParseViolation{
				File: path,
				Func: fd.Name.Name,
				Line: fset.Position(fd.Pos()).Line,
			})
		}
	}
	return out
}
