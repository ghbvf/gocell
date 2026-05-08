package archtest

import (
	"testing"
)

// TestLayerTestutil_NegativeProbe locks the classification primitives that
// power TestLayerTestutil. If the helpers regress (e.g. a refactor changes
// what counts as test infrastructure), this test fails fast with a precise
// message instead of letting the parent test pass silently because nothing
// is left to scan.
//
// Maps to the four boundary cases the parent rule must enforce:
//  1. Production file in cells/ must NOT be test infra (forbid testutil import)
//  2. testutil import path must be detected (extractTestutilRoot != "")
//  3. *test conformance suite (e.g. outboxtest) must BE test infra (allow)
//  4. tests/* path must BE test infra (allow)
//  5. Random runtime production file must NOT be test infra
func TestLayerTestutil_NegativeProbe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		path        string
		isInfra     bool
		isTestutil  bool
		description string
	}{
		{"production cells/", "cells/auditcore/foo.go", false, false, "production file must not be classified as test infra"},
		{"production runtime/", "runtime/http/router/router.go", false, false, "production runtime file must not be classified as test infra"},
		{"production adapters/", "adapters/postgres/pool.go", false, false, "production adapter file must not be classified as test infra"},
		{"testutil tree under cells", "cells/accesscore/internal/testutil/sessionrepo.go", true, false, "per-cell testutil dir is test infra"},
		{"testutil tree under pkg", "pkg/testutil/fileutil/fileutil.go", true, false, "pkg/testutil/* is test infra"},
		{"*test conformance suite", "kernel/outbox/outboxtest/conformance.go", true, false, "outboxtest is test infra (cross-pkg test helpers)"},
		{"locktest conformance", "runtime/distlock/locktest/conformance.go", true, false, "locktest is test infra"},
		{"tests/* path", "tests/e2e/internal/require/docker.go", true, false, "tests/* paths are test infra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotInfra := isTestInfraPath(tc.path)
			if gotInfra != tc.isInfra {
				t.Errorf("isTestInfraPath(%q) = %v, want %v — %s", tc.path, gotInfra, tc.isInfra, tc.description)
			}
		})
	}

	importCases := []struct {
		importPath string
		want       string
	}{
		{"pkg/testutil/fileutil", "pkg/testutil"},
		{"pkg/testutil/testtime", "pkg/testutil"},
		{"cells/accesscore/internal/testutil", "cells/accesscore/internal/testutil"},
		{"cells/accesscore/internal/testutil/sub", "cells/accesscore/internal/testutil"},
		{"tests/testutil", "tests/testutil"},
		{"runtime/foo", ""},
		{"cells/auditcore/foo", ""},
	}
	for _, tc := range importCases {
		t.Run("extract:"+tc.importPath, func(t *testing.T) {
			got := extractTestutilRoot(tc.importPath)
			if got != tc.want {
				t.Errorf("extractTestutilRoot(%q) = %q, want %q", tc.importPath, got, tc.want)
			}
		})
	}
}
