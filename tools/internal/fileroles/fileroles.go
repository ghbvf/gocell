// Package fileroles classifies module-relative file paths into the two
// disjoint roles that the GoCell archtest suite needs: production code and
// test code. Both PROD-DURATION-CONST-01 (which scans only production code)
// and TEST-TIME-LITERAL-01 (which scans only test code) consume this
// classifier as their single source of truth, so the two gates can never
// drift out of complement.
//
// "Test code" is any file whose role in the build is exclusively
// non-shipping: *_test.go (the canonical Go test convention), every
// **/conformance.go (driver-conformance suites that exercise an adapter
// under test), and every file under a recognized test-helper package
// (locktest, outboxtest, storetest, healthtest, contracttest, commandtest).
//
// "Production code" is the strict complement within the gate scope: any
// file under a top-level Go directory (cmd/, kernel/, runtime/, adapters/,
// cells/, examples/, pkg/, tests/, tools/) that is NOT test code, NOT
// vendored, NOT generated, NOT under any testdata/ tree, and NOT inside
// the archtest tooling itself.
//
// Both predicates take module-relative paths in forward-slash form
// ("kernel/outbox/consumer.go", not absolute paths or backslashes). The
// helper `Rel` provides the canonical conversion from an absolute path.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 TEST-TIME-LITERAL-01
package fileroles

import (
	"path/filepath"
	"strings"
)

// Rel converts an absolute path to a module-relative, forward-slash path
// for use with IsTestCode / IsProductionCode. Returns ("", false) when
// abs is outside modRoot or any other rel-path conversion error occurs.
func Rel(modRoot, abs string) (string, bool) {
	rel, err := filepath.Rel(modRoot, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// IsTestCode reports whether the given module-relative path is test code
// for the purposes of TEST-TIME-LITERAL-01.
//
// Hard exclusions (return false even if the path otherwise looks like
// test code):
//   - tools/archtest/ — the gate itself, including its fixtures and
//     internal self-tests
//   - vendor/, generated/ — third-party / generated content
//   - any path containing testdata/ — Go's reserved fixture directory
//
// Inclusions (return true unless caught by an exclusion above):
//   - *_test.go
//   - **/conformance.go
//   - any file under one of the recognized test-helper packages:
//     locktest, outboxtest, storetest, healthtest, contracttest, commandtest
func IsTestCode(rel string) bool {
	if rel == "" {
		return false
	}
	switch {
	case strings.HasPrefix(rel, "tools/archtest/"):
		return false
	case strings.HasPrefix(rel, "vendor/"):
		return false
	case strings.HasPrefix(rel, "generated/"):
		return false
	case strings.HasPrefix(rel, "testdata/"), strings.Contains(rel, "/testdata/"):
		return false
	}
	switch {
	case strings.HasSuffix(rel, "_test.go"):
		return true
	case strings.HasSuffix(rel, "/conformance.go"):
		return true
	case strings.Contains(rel, "/locktest/"):
		return true
	case strings.Contains(rel, "/outboxtest/"):
		return true
	case strings.Contains(rel, "/storetest/"):
		return true
	case strings.Contains(rel, "/healthtest/"):
		return true
	case strings.Contains(rel, "/contracttest/"):
		return true
	case strings.Contains(rel, "/commandtest/"):
		return true
	}
	return false
}

// IsProductionCode reports whether the given module-relative path is
// production code for the purposes of PROD-DURATION-CONST-01: a strict
// complement of IsTestCode within the gate scope.
//
// Hard exclusions (return false even when the path is not test code):
//   - tools/archtest/ — the gate tooling itself
//   - vendor/, generated/ — third-party / generated content
//   - any path containing testdata/ — Go's reserved fixture directory
//   - any /conformance.go — driver-conformance suites are test code
//   - examples/ — example projects ship documentation, not production binaries
//
// Inclusions: any other Go file under cmd/, kernel/, runtime/, adapters/,
// cells/, pkg/, tests/, tools/ that is not caught by IsTestCode.
func IsProductionCode(rel string) bool {
	if rel == "" {
		return false
	}
	switch {
	case strings.HasPrefix(rel, "tools/archtest/"):
		return false
	case strings.HasPrefix(rel, "vendor/"):
		return false
	case strings.HasPrefix(rel, "generated/"):
		return false
	case strings.HasPrefix(rel, "testdata/"), strings.Contains(rel, "/testdata/"):
		return false
	case strings.HasPrefix(rel, "examples/"):
		return false
	}
	return !IsTestCode(rel)
}
