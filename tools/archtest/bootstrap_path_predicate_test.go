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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// substringSetupAdminRE matches any call-site use of strings.Contains with "setup/admin".
var substringSetupAdminRE = regexp.MustCompile(`strings\.Contains\([^)]*"setup/admin"\)`)

// TestBootstrapPathPredicateSole verifies that strings.Contains(_, "setup/admin")
// does not appear outside the blessed kernel/metadata/bootstrap_path*.go files.
func TestBootstrapPathPredicateSole(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	// Files allowed to contain the pattern (implementation + test).
	// This archtest file is also excluded; its comments describe the pattern
	// but contain no actual strings.Contains call-sites.
	allowed := map[string]bool{
		filepath.Join(root, "kernel", "metadata", "bootstrap_path.go"):               true,
		filepath.Join(root, "kernel", "metadata", "bootstrap_path_test.go"):          true,
		filepath.Join(root, "tools", "archtest", "bootstrap_path_predicate_test.go"): true,
	}

	var violations []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden dirs and vendor.
			name := d.Name()
			if name == ".git" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if allowed[path] {
			return nil
		}

		// #nosec G304 — path is constructed from filepath.WalkDir over the module root,
		// not from user-controlled input.
		f, err := os.Open(path) // #nosec G304 — path from WalkDir, not user input
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(f)
		if closeErr := f.Close(); closeErr != nil && readErr == nil {
			return closeErr
		}
		if readErr != nil {
			return readErr
		}

		for lineNum, line := range strings.Split(string(content), "\n") {
			if substringSetupAdminRE.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations,
					rel+":"+strconv.Itoa(lineNum+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("BOOTSTRAP-PATH-PREDICATE-SOLE-01: WalkDir failed: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("BOOTSTRAP-PATH-PREDICATE-SOLE-01: found %d file(s) using strings.Contains(_, \"setup/admin\") "+
			"outside the allowed kernel/metadata/bootstrap_path*.go — use metadata.IsBootstrapPath instead:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
