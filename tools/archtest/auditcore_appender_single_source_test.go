// INVARIANT: AUDITCORE-APPENDER-SINGLE-SOURCE-01
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// auditcoreAppenderSlicePackages is the closed set of slice packages that
// must remain thin facades over cells/auditcore/internal/appender. Adding a
// new auditappend* slice requires (1) extending this list and (2) adding the
// corresponding name to appender.MustNewSpec's whitelist.
var auditcoreAppenderSlicePackages = []string{
	"cells/auditcore/slices/auditappenduser",
	"cells/auditcore/slices/auditappendconfig",
	"cells/auditcore/slices/auditappendsession",
	"cells/auditcore/slices/auditappendrole",
}

// allowedAuditcoreAppenderSliceDecls enumerates the only top-level
// declaration shapes permitted in slice packages' non-generated, non-test
// .go files. Anything else (including a struct declaration of `Service`,
// methods receiving *Service, NewService, or With*-style Option helpers)
// is a violation. This is intentionally narrow — the slice packages are
// metadata holders, not behavior implementations.
const (
	allowedTypeAlias = "type Service = appender.Service" // exact required shape
	allowedSpecVar   = "var Spec"                        // var Spec = appender.MustNewSpec(...)
)

// TestAuditcoreAppenderSliceFacadesAreThin asserts that the four
// auditappend{user,config,session,role} slice packages remain pure facades
// over cells/auditcore/internal/appender.
//
// Slice packages MAY contain (in service.go):
//   - type Service = appender.Service           (the alias is a Hard defense:
//     Go forbids methods on aliases to non-local types — slice cannot
//     redefine HandleEvent even if it tries)
//   - var Spec = appender.MustNewSpec(...)      (slice's only knob)
//   - import statements
//   - package-level doc comments
//
// Slice packages MUST NOT define:
//   - type Service struct {...}                 (would re-fork the type)
//   - func (s *Service) HandleEvent(...)        (would re-fork behavior)
//   - func NewService(...)                      (must use appender.NewService)
//   - func WithEmitter / WithTxManager (...)    (must use appender.With*)
//   - func extractActorID (or any helper that duplicates appender logic)
//
// slice_gen.go (codegen, contains the unexported eventHandlerService
// interface) is excluded from this scan — it has its own DO NOT EDIT guard.
//
// Rationale: PR #450 follow-up (PR-PR450-DEDUP) collapsed 4 ~150-line
// services into one ~150-line appender.Service. The type-system Hard
// defenses (type alias + sealed Spec) prevent re-forking by accident; this
// archtest is the Medium tripwire that fires if someone abandons the alias
// entirely (e.g. reintroduces `type Service struct {...}` in a slice
// package). Loud failure beats silent drift.
//
// AI-rebust: Medium (AST symbol-shape match against an explicit allowlist).
// Hard counterpart lives in spec.go (sealed Spec, sealed ActorMode) and in
// the type alias itself.
func TestAuditcoreAppenderSliceFacadesAreThin(t *testing.T) {
	root := findModuleRoot(t)

	scope := scanner.DirsScope(root, auditcoreAppenderSlicePackages,
		scanner.MatchRels(func(rel string) bool {
			base := filepath.Base(rel)
			// Skip generated files (own DO NOT EDIT guard) and tests.
			if strings.HasSuffix(base, "_gen.go") || strings.HasSuffix(base, "_test.go") {
				return false
			}
			return strings.HasSuffix(base, ".go")
		}),
	)

	var violations []string
	scanner.EachFile(t, scope, parser.ParseComments, func(_ *testing.T, fc scanner.FileContext) {
		violations = append(violations, scanAuditcoreAppenderSliceFile(fc)...)
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Logf("%s", v)
	}
	const failMsg = "rule AUDITCORE-APPENDER-SINGLE-SOURCE-01: slice packages " +
		"under cells/auditcore/slices/auditappend* must be thin facades over " +
		"cells/auditcore/internal/appender — only `type Service = appender." +
		"Service` and `var Spec = appender.MustNewSpec(...)` are permitted; " +
		"reintroducing local Service struct, methods, NewService, or " +
		"With*-style options re-forks behavior the appender package was " +
		"extracted to single-source"
	assert.Empty(t, violations, failMsg)
}

func scanAuditcoreAppenderSliceFile(fc scanner.FileContext) []string {
	var violations []string
	for _, decl := range fc.File.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			violations = append(violations, scanGenDeclAppender(fc, d)...)
		case *ast.FuncDecl:
			violations = append(violations, scanFuncDeclAppender(fc, d)...)
		}
	}
	return violations
}

func scanGenDeclAppender(fc scanner.FileContext, d *ast.GenDecl) []string {
	var violations []string
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.ImportSpec:
			// imports always allowed
		case *ast.TypeSpec:
			if s.Name.Name == "Service" {
				if !s.Assign.IsValid() {
					// `type Service struct {...}` or `type Service interface {...}` —
					// only `type Service = ...` (alias, Assign valid) is allowed.
					violations = append(violations, fmt.Sprintf(
						"%s:%d: AUDITCORE-APPENDER-SINGLE-SOURCE-01: forbidden `type Service` definition (must be `%s`)",
						fc.Rel, fc.Fset.Position(s.Pos()).Line, allowedTypeAlias))
				}
			}
		case *ast.ValueSpec:
			// `var Spec = appender.MustNewSpec(...)` and `const ...` are allowed.
			// We deliberately do not enforce the RHS shape — appender.MustNewSpec's
			// own whitelist gives the Hard defense at runtime/test-time.
		}
	}
	return violations
}

func scanFuncDeclAppender(fc scanner.FileContext, d *ast.FuncDecl) []string {
	pos := fc.Fset.Position(d.Pos()).Line

	// Methods: any receiver named *Service or Service is forbidden — slice
	// packages must not attach behavior to the (aliased) Service type. Go's
	// compiler already rejects methods on aliases to non-local types, but
	// this catches the abandonment case where someone reintroduces a local
	// `type Service struct {}` and then attaches methods.
	if d.Recv != nil && len(d.Recv.List) == 1 {
		recvType := receiverTypeName(d.Recv.List[0].Type)
		if recvType == "Service" {
			return []string{fmt.Sprintf(
				"%s:%d: AUDITCORE-APPENDER-SINGLE-SOURCE-01: forbidden method on Service (slice must not extend the appender.Service alias)",
				fc.Rel, pos)}
		}
		// Methods on other types are allowed (none expected today, but not banned).
		return nil
	}

	// Top-level functions: NewService and With* helpers are forbidden.
	switch {
	case d.Name.Name == "NewService":
		return []string{fmt.Sprintf(
			"%s:%d: AUDITCORE-APPENDER-SINGLE-SOURCE-01: forbidden `func NewService` (call appender.NewService directly from cell.go)",
			fc.Rel, pos)}
	case strings.HasPrefix(d.Name.Name, "With"):
		return []string{fmt.Sprintf(
			"%s:%d: AUDITCORE-APPENDER-SINGLE-SOURCE-01: forbidden `func %s` (call appender.%s directly from cell.go)",
			fc.Rel, pos, d.Name.Name, d.Name.Name)}
	}
	// Other top-level functions are flagged so the next reviewer notices —
	// slice packages are metadata holders only.
	return []string{fmt.Sprintf(
		"%s:%d: AUDITCORE-APPENDER-SINGLE-SOURCE-01: unexpected top-level "+
			"func %s; slice packages should hold only `type Service = "+
			"appender.Service` + `var Spec = ...`",
		fc.Rel, pos, d.Name.Name)}
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}
