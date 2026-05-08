// BOOTSTRAP-PATH-PREDICATE-SOLE-01
//
// Invariant: strings.Contains(_, "setup/admin") must not appear anywhere in
// the codebase except kernel/metadata/bootstrap_path.go (implementation + comments)
// and kernel/metadata/bootstrap_path_test.go. All judgment points must call
// metadata.IsBootstrapPath instead.
//
// ref: postmortem 202605060030 §5.3 / ADR §D4
package archtest

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// substringSetupAdminRE matches any call-site use of strings.Contains with "setup/admin".
var substringSetupAdminRE = regexp.MustCompile(`strings\.Contains\([^)]*"setup/admin"\)`)

// TestBootstrapPathPredicateSole verifies that strings.Contains(_, "setup/admin")
// does not appear outside the blessed kernel/metadata/bootstrap_path*.go files.
func TestBootstrapPathPredicateSole(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := scanner.ModuleScope(root,
		scanner.IncludeTests(),
		scanner.ExcludeRels(
			"kernel/metadata/bootstrap_path.go",
			"kernel/metadata/bootstrap_path_test.go",
			"tools/archtest/bootstrap_path_predicate_test.go",
		),
	)

	files, err := scope.Files()
	if err != nil {
		t.Fatalf("BOOTSTRAP-PATH-PREDICATE-SOLE-01: scope.Files failed: %v", err)
	}

	var violations []string
	for _, absPath := range files {
		// #nosec G304 — path comes from scope.Files over the module root, not user input
		content, readErr := os.ReadFile(absPath) // #nosec G304
		if readErr != nil {
			t.Fatalf("BOOTSTRAP-PATH-PREDICATE-SOLE-01: read %s: %v", absPath, readErr)
		}
		rel := strings.TrimPrefix(absPath, root+string(os.PathSeparator))
		for lineNum, line := range strings.Split(string(content), "\n") {
			if substringSetupAdminRE.MatchString(line) {
				violations = append(violations,
					rel+":"+strconv.Itoa(lineNum+1)+": "+strings.TrimSpace(line))
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("BOOTSTRAP-PATH-PREDICATE-SOLE-01: found %d file(s) using strings.Contains(_, \"setup/admin\") "+
			"outside the allowed kernel/metadata/bootstrap_path*.go — use metadata.IsBootstrapPath instead:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
