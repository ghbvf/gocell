package errcode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodeToStatus_Exhaustive parses pkg/errcode/errcode.go with go/ast,
// extracts every Code constant, and verifies it has an entry in codeToStatus.
// This fails loudly when a new errcode.Code is added without registering an
// HTTP status mapping, forcing the developer to make a conscious choice.
func TestCodeToStatus_Exhaustive(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	errcodeFile := filepath.Join(filepath.Dir(thisFile), "errcode.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, errcodeFile, nil, 0)
	require.NoError(t, err, "failed to parse errcode.go")

	// Collect string values of all `const ... Code = "..."` declarations.
	var codes []string
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "Code" {
				continue
			}
			for _, val := range vs.Values {
				lit, ok := val.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				codes = append(codes, s)
			}
		}
	}

	require.NotEmpty(t, codes, "should find Code constants in errcode.go")

	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			_, registered := codeToStatus[Code(code)]
			assert.True(t, registered,
				"errcode.Code %q has no entry in codeToStatus — add it to the map in status.go", code)
		})
	}
}
