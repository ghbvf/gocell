package archtest

// INVARIANT: PG-CONSTRUCTOR-MUST-FREE-01
//
// postgres_constructor_error_first_test.go enforces PG-CONSTRUCTOR-MUST-FREE-01:
// no non-test .go file in adapters/postgres/ may declare an exported function
// whose name starts with "Must" and whose first word after "Must" is "New"
// (i.e., MustNew* constructors). After B2-A-11, all postgres constructors are
// error-first; the Must* panic wrappers have been removed.
//
// Rule: scan every non-_test.go file under adapters/postgres/ for top-level
// exported FuncDecl whose name matches ^MustNew. Report each one.

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const rulePGConstructorMustFree01 = "PG-CONSTRUCTOR-MUST-FREE-01"

// TestPGConstructorMustFree01 walks adapters/postgres/ non-test Go files and
// reports any exported MustNew* function declaration.
func TestPGConstructorMustFree01(t *testing.T) {
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"adapters/postgres"})

	type violation struct {
		file string
		line int
		name string
	}
	var violations []violation

	// DirsScope without IncludeTests() already excludes *_test.go files.
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				name := fd.Name.Name
				// exported MustNew* at package level (no receiver)
				if fd.Recv != nil {
					return
				}
				if !strings.HasPrefix(name, "MustNew") {
					return
				}
				pos := p.Fset.Position(fd.Pos())
				violations = append(violations, violation{
					file: p.Rel(file),
					line: pos.Line,
					name: name,
				})
			})
		}
		return nil
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", rulePGConstructorMustFree01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  %s — MustNew* constructors are banned in adapters/postgres/ (B2-A-11)", v.file, v.line, v.name)
		}
	}
	assert.Empty(t, violations,
		"%s: adapters/postgres/ must not export MustNew* constructors; use error-first NewXxx instead (B2-A-11)",
		rulePGConstructorMustFree01)
}
