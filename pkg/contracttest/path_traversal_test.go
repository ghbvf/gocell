package contracttest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// fakeT captures Fatalf for path-traversal negative tests so the test binary
// doesn't actually fail; we just inspect whether the helper rejected the
// input.
type fakeT struct {
	testing.TB
	failed bool
	msg    string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = strings.TrimSpace(fmt.Sprintf(format, args...))
}

func TestPathWithinAllowList(t *testing.T) {
	contractDir := filepath.Join(testdataRoot(), "http", "test", "valid", "v1")

	cases := []struct {
		name     string
		filename string
		wantOK   bool
		desc     string
	}{
		{"in_dir_simple", "request.schema.json", true, "same-dir schema reference"},
		{
			name: "shared_cross_ref",
			// 4-up navigation matches the canonical real-world pattern
			// (contracts/<kind>/<domain>/<v>/ → ../../../../shared/...).
			filename: "../../../../shared/errors/error-response-v1.schema.json",
			wantOK:   true,
			desc:     "valid cross-contract shared schema under sibling contractsRoot/shared",
		},
		{
			name:     "escape_to_repo_outside_rejected",
			filename: strings.Repeat("../", 30) + "etc/passwd",
			wantOK:   false,
			desc:     "navigating above any contracts/ ancestor fails",
		},
		{
			name:     "inside_repo_but_not_shared_rejected",
			filename: "../../../../../README.md",
			wantOK:   false,
			desc:     "outside contractsRoot/shared subtree fails (escapes contractsRoot)",
		},
		{
			name:     "escape_via_shared_then_up_rejected",
			filename: "../../../../shared/../../README.md",
			wantOK:   false,
			desc:     "shared/ prefix that re-escapes upward via .. fails after Clean",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeT{TB: t}
			ok := pathWithinAllowList(ft, filepath.Clean(contractDir), filepath.Clean(filepath.Join(contractDir, tc.filename)))
			if tc.wantOK {
				if !ok {
					t.Errorf("%s: expected accept, got reject; helper Fatalf=%q", tc.desc, ft.msg)
				}
				if ft.failed {
					t.Errorf("%s: helper called Fatalf for accepted input: %q", tc.desc, ft.msg)
				}
			} else if ok && !ft.failed {
				t.Errorf("%s: expected reject, got accept", tc.desc)
			}
		})
	}
}

// Integration coverage: existing extra_schema_refs_test and error_response_test
// exercise compileSchemaFile end-to-end via Load/ValidateErrorResponse with the
// shared/ cross-ref pattern; pathWithinAllowList unit tests above cover the
// security boundary in isolation. We deliberately skip wrapping fakeT around
// compileSchemaFile because *testing.T.Fatalf aborts via runtime.Goexit (which
// fakeT cannot replicate without spawning its own goroutine), and the negative
// security checks are fully exercised at the unit layer.
