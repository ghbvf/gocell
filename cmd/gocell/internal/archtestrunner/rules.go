package archtestrunner

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// ruleIndex maps INVARIANT rule IDs to the test function names that assert them.
// Built by scanning INVARIANT anchor comments in tools/archtest/*_test.go headers.
type ruleIndex map[string][]string

// testFileMeta holds the file path and rule IDs for a single test file.
type testFileMeta struct {
	file  string   // repo-relative path, e.g. "tools/archtest/layer_test.go"
	rules []string // INVARIANT anchor IDs from the file header
}

// testMetaMap maps test function names to their owning file metadata.
type testMetaMap map[string]testFileMeta

// archtestPkgDir is the top-level archtest directory relative to workspace root.
const archtestPkgDir = "tools/archtest"

// buildRuleIndex scans top-level tools/archtest/*_test.go files for
// // INVARIANT: <ID> header anchors and returns the reverse index:
// rule ID → []testFunctionName.
//
// Only the top-level directory is scanned (not internal/ subdirs), matching
// the shell's discovery behavior.
func buildRuleIndex(workspaceRoot string) (ruleIndex, error) {
	idx := make(ruleIndex)
	dir := filepath.Join(workspaceRoot, archtestPkgDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, fmt.Errorf("archtestrunner: read archtest dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		rules, testFuncs, err := parseTestFile(path)
		if err != nil {
			return nil, fmt.Errorf("archtestrunner: parse %s: %w", name, err)
		}
		for _, ruleID := range rules {
			idx[ruleID] = append(idx[ruleID], testFuncs...)
		}
	}
	return idx, nil
}

// buildFileMetaMap scans top-level tools/archtest/*_test.go files and returns
// a map from test function name to its file path and rule IDs. Used to populate
// TestResult.File and TestResult.Rules.
func buildFileMetaMap(workspaceRoot string) (testMetaMap, error) {
	meta := make(testMetaMap)
	dir := filepath.Join(workspaceRoot, archtestPkgDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return meta, nil
		}
		return nil, fmt.Errorf("archtestrunner: read archtest dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		absPath := filepath.Join(dir, name)
		repoRelPath := filepath.Join(archtestPkgDir, name)

		rules, testFuncs, err := parseTestFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("archtestrunner: parse %s: %w", name, err)
		}
		for _, fn := range testFuncs {
			meta[fn] = testFileMeta{
				file:  repoRelPath,
				rules: rules,
			}
		}
	}
	return meta, nil
}

// parseTestFile parses a single *_test.go file and returns:
//   - the INVARIANT rule IDs from the file header comment group
//   - the top-level TestXxx function names
//
// Uses go/parser with ParseComments to read the build tag and header anchors.
// The file is parsed directly (no type-checking needed).
func parseTestFile(path string) (rules []string, testFuncs []string, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}

	rules = extractInvariantIDs(f.Comments)
	testFuncs = extractTestFuncNames(f)
	return rules, testFuncs, nil
}

// extractInvariantIDs scans all comment groups in a file for
// "INVARIANT: <ID>" lines and returns the unique IDs found.
//
// Supports both inline form:
//
//	// INVARIANT: FOO-01
//
// and list form:
//
//	//   - INVARIANT: FOO-01
func extractInvariantIDs(groups []*ast.CommentGroup) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, group := range groups {
		for _, comment := range group.List {
			line := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
			// Strip optional "- " prefix (list form).
			line = strings.TrimPrefix(line, "- ")
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "INVARIANT:") {
				continue
			}
			id := strings.TrimSpace(strings.TrimPrefix(line, "INVARIANT:"))
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// extractTestFuncNames returns all top-level func TestXxx(*testing.T) names.
func extractTestFuncNames(f *ast.File) []string {
	var names []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv != nil {
			continue // method, not a top-level function
		}
		name := fn.Name.Name
		if !strings.HasPrefix(name, "Test") {
			continue
		}
		// Must take *testing.T as the first (and only) parameter.
		if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
			continue
		}
		names = append(names, name)
	}
	return names
}

// selectByRule returns the intersection of the rule's test functions with the
// discovered set. Returns an error if the rule ID is not found in the index.
func selectByRule(idx ruleIndex, ruleID string, discovered []string) ([]string, error) {
	ruleFuncs, ok := idx[ruleID]
	if !ok {
		return nil, fmt.Errorf(
			"archtestrunner: unknown rule: %s (no INVARIANT anchor found in tools/archtest/);"+
				" list rule IDs with: grep -rn '// INVARIANT:' tools/archtest/*_test.go",
			ruleID,
		)
	}

	ruleSet := make(map[string]bool, len(ruleFuncs))
	for _, fn := range ruleFuncs {
		ruleSet[fn] = true
	}

	var result []string
	for _, name := range discovered {
		if ruleSet[name] {
			result = append(result, name)
		}
	}
	return result, nil
}
