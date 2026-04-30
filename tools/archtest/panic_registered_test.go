package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

type panicRegisteredScope struct {
	FuncName     string
	AllowMust    bool
	WhitelistKey string
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
			name: "package initializer panic fails",
			src: `package p
var _ = func() string {
	panic("boom")
}()`,
			wantLines: []int{3},
		},
		{
			name: "nested function literal panic under Must function fails",
			src: `package p
func MustNew() func() {
	return func() {
		panic("boom")
	}
}`,
			wantLines: []int{4},
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
	var scopes []panicRegisteredScope
	var pushedScopes []bool
	funcLitCount := 0

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			if len(pushedScopes) == 0 {
				return true
			}
			didPush := pushedScopes[len(pushedScopes)-1]
			pushedScopes = pushedScopes[:len(pushedScopes)-1]
			if didPush {
				scopes = scopes[:len(scopes)-1]
			}
			return true
		}

		didPush := false
		switch node := n.(type) {
		case *ast.FuncDecl:
			funcName := panicRegisteredFuncName(node)
			scopes = append(scopes, panicRegisteredScope{
				FuncName:     funcName,
				AllowMust:    strings.HasPrefix(node.Name.Name, "Must"),
				WhitelistKey: rel + "::" + funcName,
			})
			didPush = true
		case *ast.FuncLit:
			funcLitCount++
			scopes = append(scopes, panicRegisteredScope{
				FuncName: panicRegisteredFuncLiteralName(scopes, funcLitCount),
			})
			didPush = true
		case *ast.CallExpr:
			if isPanicCallExpr(node) {
				violations = appendPanicRegisteredViolation(violations, fset, rel, node.Pos(), scopes, whitelist, usedWhitelist)
			}
		}
		pushedScopes = append(pushedScopes, didPush)
		return true
	})
	return violations
}

func appendPanicRegisteredViolation(
	violations []panicRegisteredViolation,
	fset *token.FileSet,
	rel string,
	pos token.Pos,
	scopes []panicRegisteredScope,
	whitelist map[string]string,
	usedWhitelist map[string]bool,
) []panicRegisteredViolation {
	scope := panicRegisteredScope{FuncName: "<package initializer>"}
	if len(scopes) > 0 {
		scope = scopes[len(scopes)-1]
	}
	if scope.AllowMust {
		return violations
	}
	if scope.WhitelistKey != "" {
		if _, ok := whitelist[scope.WhitelistKey]; ok {
			usedWhitelist[scope.WhitelistKey] = true
			return violations
		}
	}
	return append(violations, panicRegisteredViolation{
		File:     rel,
		Line:     fset.Position(pos).Line,
		FuncName: scope.FuncName,
		Reason:   "panic() is neither in a Must* function nor registered in the architectural panic ADR",
	})
}

func panicRegisteredFuncLiteralName(scopes []panicRegisteredScope, index int) string {
	parent := "<package>"
	if len(scopes) > 0 {
		parent = scopes[len(scopes)-1].FuncName
	}
	return parent + ".func" + strconv.Itoa(index)
}

func isPanicCallExpr(call *ast.CallExpr) bool {
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "panic"
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
	assert.Equal(t, 4, len(goKeys),
		"%s: architectural panic whitelist must contain exactly the ADR-approved permanent entries", rulePanicRegistered01)

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
	for line := range strings.SplitSeq(string(data), "\n") {
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
