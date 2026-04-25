package archtest

// HEALTH-AGG-01: Any exported type in runtime/ or adapters/ that exposes a
// Checkers() or HealthCheckers() method must implement the full
// kernellifecycle.ManagedResource interface (i.e., also have Worker() and
// Close() methods). This prevents the "register health checkers but forget the
// rest of the lifecycle contract" class of bugs that WithRelayHealth
// represented.
//
// Implementation: pure AST analysis via go/parser over the source tree.
// This avoids introducing golang.org/x/tools as a dependency while matching
// the existing archtest style (file-system walk + go list -json).
//
// Enforcement scope: runtime/, adapters/ packages only.
// Excluded: cells/, kernel/cell/ — HealthCheckersContributor is a different
// interface that intentionally doesn't bundle Worker/Close.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typeMethodSet collects the method names defined on exported types in a
// directory tree (only direct method declarations, not embedded/promoted).
type typeMethodSet struct {
	// methods maps TypeName → set of method names declared in the source.
	methods map[string]map[string]struct{}
}

func newTypeMethodSet() *typeMethodSet {
	return &typeMethodSet{methods: make(map[string]map[string]struct{})}
}

func (s *typeMethodSet) add(typeName, methodName string) {
	if _, ok := s.methods[typeName]; !ok {
		s.methods[typeName] = make(map[string]struct{})
	}
	s.methods[typeName][methodName] = struct{}{}
}

func (s *typeMethodSet) has(typeName, methodName string) bool {
	ms, ok := s.methods[typeName]
	if !ok {
		return false
	}
	_, ok = ms[methodName]
	return ok
}

// collectTypeMethods walks all .go files under root (skipping *_test.go) and
// collects (receiver-type, method-name) pairs for exported types.
//
// Known limitation: promoted methods from embedded fields are NOT detected.
// For example, if TypeA embeds TypeB and TypeB has Close(), the AST walk
// records Close() on TypeB but not on TypeA. Enforcement therefore relies on
// direct method declarations. Types that satisfy ManagedResource solely via
// embedding must declare a thin delegation method in their own source file to
// be detected. This is an accepted limitation for the current pure-AST approach.
func collectTypeMethods(t *testing.T, root string) *typeMethodSet {
	t.Helper()
	s := newTypeMethodSet()
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip test files and vendor.
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", "generated":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// Skip unparseable files — they'll surface in go build.
			return nil
		}

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			// Extract receiver type name (handle *T and T).
			recvType := receiverTypeName(fn.Recv.List[0].Type)
			if recvType == "" {
				continue
			}
			// Only exported types and methods.
			if !ast.IsExported(recvType) || !fn.Name.IsExported() {
				continue
			}
			s.add(recvType, fn.Name.Name)
		}
		return nil
	})
	require.NoError(t, err, "walking source tree failed")
	return s
}

// receiverTypeName extracts the base type name from a receiver type expression.
// Handles *T (StarExpr) and T (Ident).
func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		// generic: T[P] — extract T.
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// isManagedResource returns true if the type has all three ManagedResource methods:
// Checkers(), Worker(), and Close().
func isManagedResource(s *typeMethodSet, typeName string) bool {
	return s.has(typeName, "Checkers") &&
		s.has(typeName, "Worker") &&
		s.has(typeName, "Close")
}

// exposesHealthCheckerMethod returns true if the type has Checkers() or
// HealthCheckers() — the two spellings that signal health-checking intent.
//
// Note: "Health(ctx)" (e.g. adapters/postgres.Pool.Health) is intentionally
// NOT included in this word list. Pool.Health is a DB connectivity probe with
// different semantics; Pool is wrapped by adapters/postgres.PGResource which
// implements the full ManagedResource contract. Adding "Health" to this list
// would incorrectly flag Pool as needing Worker/Close.
func exposesHealthCheckerMethod(s *typeMethodSet, typeName string) bool {
	return s.has(typeName, "Checkers") || s.has(typeName, "HealthCheckers")
}

// TestHealthCheckersImpliesManagedResource (HEALTH-AGG-01) asserts that every
// exported type in runtime/ or adapters/ that exposes Checkers() or
// HealthCheckers() also implements the full ManagedResource contract
// (Checkers + Worker + Close).
func TestHealthCheckersImpliesManagedResource(t *testing.T) {
	root := findModuleRoot(t)

	// Enforce only runtime/ and adapters/ — exclude cells/ and kernel/cell/.
	enforcedLayers := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "adapters"),
	}

	var violations []string

	for _, layerRoot := range enforcedLayers {
		if _, err := os.Stat(layerRoot); os.IsNotExist(err) {
			continue
		}
		s := collectTypeMethods(t, layerRoot)

		for typeName := range s.methods {
			if !exposesHealthCheckerMethod(s, typeName) {
				continue
			}
			if !isManagedResource(s, typeName) {
				// Determine which methods are missing for a useful message.
				var missing []string
				if !s.has(typeName, "Worker") {
					missing = append(missing, "Worker()")
				}
				if !s.has(typeName, "Close") {
					missing = append(missing, "Close()")
				}
				// If the type only has HealthCheckers() (old spelling) but not
				// Checkers(), it also needs the rename.
				if s.has(typeName, "HealthCheckers") && !s.has(typeName, "Checkers") {
					missing = append(missing, "Checkers() [rename from HealthCheckers]")
				}
				rel := strings.TrimPrefix(layerRoot, root+string(filepath.Separator))
				violations = append(violations,
					rel+": "+typeName+" exposes health checker methods but is missing: "+
						strings.Join(missing, ", ")+" (HEALTH-AGG-01: must implement ManagedResource)")
			}
		}
	}

	assert.Empty(t, violations, "HEALTH-AGG-01 violation: types exposing health checker methods must implement kernellifecycle.ManagedResource")
}
