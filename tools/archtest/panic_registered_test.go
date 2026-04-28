package archtest

import (
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

const rulePanicRegistered01 = "PANIC-REGISTERED-01"

// architecturalPanicWhitelist maps "<rel-path>::<funcName>" or
// "<rel-path>::<Receiver>.<methodName>" to an ADR-pinned justification. Keep
// this map exactly aligned with docs/architecture/202604270030-architectural-panic-whitelist.md.
var architecturalPanicWhitelist = map[string]string{
	"kernel/wrapper/lifecycle.go::recoverAndFinishWithRedactor":              "re-panics from defer recover so outer Recovery middleware can serialize the panic",
	"runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure": "re-panics from defer recover after reporting circuit-breaker failure",
	"adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback":        "re-panics after top-level transaction rollback so caller panic semantics are preserved",
	"adapters/postgres/tx_manager.go::repanicAfterSavepointRollback":         "re-panics after savepoint rollback so nested transaction panic semantics are preserved",
}

type panicRegisteredViolation struct {
	File     string
	Line     int
	FuncName string
	Reason   string
}

func TestPanicRegistered(t *testing.T) {
	root := findModuleRoot(t)

	violations, usedWhitelist, err := scanRootForPanicRegisteredViolations(root, architecturalPanicWhitelist)
	require.NoError(t, err)
	assertPanicWhitelistMatchesADR(t, root, usedWhitelist)

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", rulePanicRegistered01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  %s — %s", v.File, v.Line, v.FuncName, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: production panic() calls must be either inside Must* functions or ADR-registered permanent exceptions",
		rulePanicRegistered01)
}

func TestPanicRegisteredScannerFixtures(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		rel       string
		whitelist map[string]string
		wantLines []int
		wantUsed  []string
	}{
		{
			name: "ordinary function panic fails",
			src: `package p
func New() {
	panic("boom")
}`,
			wantLines: []int{3},
		},
		{
			name: "nested function literal panic fails under non Must function",
			src: `package p
func New() {
	fn := func() { panic("boom") }
	fn()
}`,
			wantLines: []int{3},
		},
		{
			name: "Must function panic passes",
			src: `package p
func MustNew() {
	panic("boom")
}`,
		},
		{
			name: "Must method panic passes",
			src: `package p
type T struct{}
func (*T) MustNew() {
	panic("boom")
}`,
		},
		{
			name: "init panic is not auto exempt",
			src: `package p
func init() {
	panic("boom")
}`,
			wantLines: []int{3},
		},
		{
			name: "recover re panic is not auto exempt",
			src: `package p
func Run() {
	defer func() {
		if r := recover(); r != nil {
			panic(r)
		}
	}()
}`,
			wantLines: []int{5},
		},
		{
			name: "ADR whitelisted function passes and marks whitelist used",
			src: `package p
func registered() {
	panic("boom")
}`,
			rel: "runtime/example.go",
			whitelist: map[string]string{
				"runtime/example.go::registered": "fixture",
			},
			wantUsed: []string{"runtime/example.go::registered"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rel := tc.rel
			if rel == "" {
				rel = "p/p.go"
			}
			whitelist := tc.whitelist
			if whitelist == nil {
				whitelist = map[string]string{}
			}
			violations, used := scanSourceForPanicRegisteredViolations(t, tc.src, rel, whitelist)
			var gotLines []int
			for _, v := range violations {
				gotLines = append(gotLines, v.Line)
			}
			assert.Equal(t, tc.wantLines, gotLines)
			assert.Equal(t, sortedStrings(tc.wantUsed), sortedStrings(mapKeys(used)))
		})
	}
}

func scanSourceForPanicRegisteredViolations(t *testing.T, src, rel string, whitelist map[string]string) ([]panicRegisteredViolation, map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution|parser.ParseComments)
	require.NoError(t, err)
	used := map[string]bool{}
	return scanPanicRegisteredAST(fset, file, rel, whitelist, used), used
}

func scanRootForPanicRegisteredViolations(root string, whitelist map[string]string) ([]panicRegisteredViolation, map[string]bool, error) {
	usedWhitelist := map[string]bool{}
	var violations []panicRegisteredViolation
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipPanicRegisteredDir(root, path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			return err
		}
		violations = append(violations, scanPanicRegisteredAST(fset, file, rel, whitelist, usedWhitelist)...)
		return nil
	})
	return violations, usedWhitelist, err
}

func scanPanicRegisteredAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	whitelist map[string]string,
	usedWhitelist map[string]bool,
) []panicRegisteredViolation {
	var violations []panicRegisteredViolation
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		var panicPositions []token.Pos
		findPanicCalls(fd.Body, func(callPos token.Pos) {
			panicPositions = append(panicPositions, callPos)
		})
		if len(panicPositions) == 0 {
			continue
		}
		if strings.HasPrefix(fd.Name.Name, "Must") {
			continue
		}
		funcName := panicRegisteredFuncName(fd)
		key := rel + "::" + funcName
		if _, ok := whitelist[key]; ok {
			usedWhitelist[key] = true
			continue
		}
		for _, pos := range panicPositions {
			violations = append(violations, panicRegisteredViolation{
				File:     rel,
				Line:     fset.Position(pos).Line,
				FuncName: funcName,
				Reason:   "panic() is neither in a Must* function nor registered in the architectural panic ADR",
			})
		}
	}
	return violations
}

func skipPanicRegisteredDir(root, path, name string) bool {
	switch name {
	case ".git", "vendor", "worktrees", "generated", "node_modules", "testdata":
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return filepath.ToSlash(rel) == "tools/archtest"
}

func panicRegisteredFuncName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	if recv := receiverTypeName(fd.Recv.List[0].Type); recv != "" {
		return recv + "." + fd.Name.Name
	}
	return fd.Name.Name
}

func assertPanicWhitelistMatchesADR(t *testing.T, root string, usedWhitelist map[string]bool) {
	t.Helper()
	goKeys := sortedStrings(mapKeys(architecturalPanicWhitelist))
	adrKeys := sortedStrings(readPanicWhitelistKeysFromADR(t, root))

	assert.Equal(t, adrKeys, goKeys,
		"%s: ADR whitelist table must exactly match architecturalPanicWhitelist", rulePanicRegistered01)
	assert.LessOrEqual(t, len(goKeys), 5,
		"%s: architectural panic whitelist must stay small and permanent", rulePanicRegistered01)

	var unused []string
	for _, key := range goKeys {
		if !usedWhitelist[key] {
			unused = append(unused, key)
		}
	}
	assert.Empty(t, unused,
		"%s: stale architectural panic whitelist entries must be removed from code and ADR", rulePanicRegistered01)
}

func readPanicWhitelistKeysFromADR(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "docs", "architecture", "202604270030-architectural-panic-whitelist.md")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var keys []string
	inSection := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "### 4. Hardcoded ADR-pinned whitelist"):
			inSection = true
			continue
		case inSection && strings.HasPrefix(line, "### "):
			return keys
		case !inSection:
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") || strings.Contains(trimmed, "---") || strings.Contains(trimmed, "Function") {
			continue
		}
		cols := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cols) < 2 {
			continue
		}
		key := strings.TrimSpace(cols[1])
		key = strings.Trim(key, "`")
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
