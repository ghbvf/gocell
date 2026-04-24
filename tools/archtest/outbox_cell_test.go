package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	outboxCellRuleRawOption            = "OUTBOX-CELL-01_no_raw_publisher_writer_option"
	outboxCellGlobReadablePattern      = "cells/**/cell.go"
	outboxCellForbiddenOptionPublisher = "WithPublisher"
	outboxCellForbiddenOptionWriter    = "WithOutboxWriter"
)

// outboxCellViolation records a rule breach on a Cell file.
type outboxCellViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v outboxCellViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

// TestCellsDoNotExposeRawOutboxOptions asserts that Cell packages stop
// exposing exported Option functions that take raw outbox.Publisher /
// outbox.Writer dependencies directly. The Cell-boundary contract
// established by PR-A5c is:
//
//   - WithEmitter(outbox.Emitter) — pre-composed emitter injection.
//   - WithOutboxDeps(pub, writer) — raw deps accumulated and composed
//     at Init() time via cell.ResolveEmitter.
//
// Standalone WithPublisher / WithOutboxWriter options let composition
// roots wire raw dependencies without going through the Cell-boundary
// emitter pipeline, which undoes the archtest guard for service-layer
// rules OUTBOX-SERVICE-01..05.
//
// ref: kubernetes/client-go rest.RESTClientFor — raw config consumed
//
//	by factory; typed client never re-exposes raw fields.
//
// Scope: scans every cells/**/cell.go declared in the repo (production
// cells only — tests and example cells are skipped; see isCellFile).
func TestCellsDoNotExposeRawOutboxOptions(t *testing.T) {
	root := findModuleRoot(t)

	violations := checkCellOutboxOptionRules(t, root)
	byRule := groupOutboxCellViolations(violations)

	if len(violations) > 0 {
		t.Logf("Found %d cell outbox architecture violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	t.Run("OUTBOX-CELL-01_no_raw_publisher_writer_option", func(t *testing.T) {
		assert.Empty(t, byRule[outboxCellRuleRawOption],
			"%s must not export Option functions named %s or %s; use WithEmitter / WithOutboxDeps",
			outboxCellGlobReadablePattern,
			outboxCellForbiddenOptionPublisher,
			outboxCellForbiddenOptionWriter)
	})
}

func checkCellOutboxOptionRules(t *testing.T, root string) []outboxCellViolation {
	t.Helper()

	files, err := findCellFiles(root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no %s files found", outboxCellGlobReadablePattern)

	var violations []outboxCellViolation
	for _, file := range files {
		fileViolations, err := checkCellOutboxOptionFile(root, file)
		require.NoError(t, err)
		violations = append(violations, fileViolations...)
	}
	return violations
}

// findCellFiles walks cells/**/cell.go (production cells only — excludes
// cells/**/internal/, cells/**/slices/**/cell.go if any, vendor, .git, and
// *_test.go shadows).
func findCellFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "cells"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "slices", "internal":
				return filepath.SkipDir
			}
			return nil
		}
		if isCellFile(root, path) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// isCellFile matches cells/<cellname>/cell.go exactly (not cell_init.go,
// cell_routes.go, cell_providers.go, or *_test.go).
func isCellFile(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "cells/") {
		return false
	}
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	// Exactly cells/<name>/cell.go (no further subpath).
	parts := strings.Split(rel, "/")
	return len(parts) == 3 && parts[2] == "cell.go"
}

func checkCellOutboxOptionFile(root, path string) ([]outboxCellViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	var violations []outboxCellViolation
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv != nil {
			// Method on a receiver; the rule targets top-level Option
			// factory functions only (no receiver).
			continue
		}
		name := fn.Name.Name
		if name != outboxCellForbiddenOptionPublisher && name != outboxCellForbiddenOptionWriter {
			continue
		}
		if !fn.Name.IsExported() {
			continue
		}
		violations = append(violations, outboxCellViolation{
			Rule:    outboxCellRuleRawOption,
			File:    rel,
			Line:    fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf("Cell must not export Option %q; use WithEmitter or WithOutboxDeps instead", name),
		})
	}
	return violations, nil
}

func groupOutboxCellViolations(violations []outboxCellViolation) map[string][]string {
	byRule := make(map[string][]string)
	for _, v := range violations {
		byRule[v.Rule] = append(byRule[v.Rule], v.String())
	}
	return byRule
}
