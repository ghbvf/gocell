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
	"os"
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

	httpRoot := filepath.Join(root, "generated", "contracts", "http")
	require.DirExists(t, httpRoot, "generated/contracts/http must exist; run `gocell generate contract --all`")
	handlers := walkHandlerGenFiles(t, httpRoot)
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

// TestScanInlineLimitParse_DetectsViolation is the reverse / self-validation
// test: it writes a synthetic handler_gen.go that intentionally pairs
// strconv.ParseInt with a "limit" string literal in the same function body
// and asserts that scanInlineLimitParse flags it. Without this test, a future
// refactor that silently broke the AST walk (e.g. inverted the condition,
// dropped the BasicLit branch) would let real violations slip past the
// archtest while the positive test still passed (because real generated
// handlers always conform).
func TestScanInlineLimitParse_DetectsViolation(t *testing.T) {
	dir := t.TempDir()
	violatingPath := filepath.Join(dir, "handler_gen.go")
	body := `package fixture

import (
	"net/http"
	"strconv"
)

func handle(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = v
	}
}
`
	require.NoError(t, os.WriteFile(violatingPath, []byte(body), 0o600))

	violations := scanInlineLimitParse(t, violatingPath)
	require.Len(t, violations, 1, "scanner must detect the synthetic violation")
	require.Equal(t, "handle", violations[0].Func)
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
