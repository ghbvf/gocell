package metadata

import (
	"regexp"
	"strings"
)

// bootstrapPathRE matches the bootstrap admin endpoint pattern:
// /api/v{N}/{cell}/setup/admin (version-agnostic, case-sensitive).
var bootstrapPathRE = regexp.MustCompile(`^/api/v\d+/[^/]+/setup/admin$`)

// IsBootstrapPath reports whether path matches the bootstrap admin endpoint
// `/api/v{N}/{cell}/setup/admin` (version-agnostic).
//
// Matching uses exact segment comparison, not substring, to prevent false
// positives on paths like /foo/setup/admin/bar or /api/v1/setup/admin/foo.
// This is GoCell's single bootstrap-path predicate; all judgment points
// (FMT-28, archtest, runtime FinalizeAuth) must call this function exclusively.
//
// Accepted:
//
//	/api/v1/access/setup/admin
//	/api/v2/access/setup/admin
//	/api/v1/anycell/setup/admin
//
// Rejected (fail-closed):
//
//	""                               (empty string)
//	"api/v1/access/setup/admin"      (missing leading /)
//	"/api/v1/setup/admin/foo"        (substring guard: missing cell segment)
//	"/foo/setup/admin/bar"           (substring guard)
//	"/api/v1/access/setup/admin/foo" (trailing extra segment)
//
// ref: postmortem 202605060030 §5.3 / ADR §D4
func IsBootstrapPath(path string) bool {
	return bootstrapPathRE.MatchString(path)
}

// IsInternalHTTPPath reports whether path targets the internal HTTP listener
// prefix where caller-cell identity can be enforced.
func IsInternalHTTPPath(path string) bool {
	return path == "/internal/v1" || strings.HasPrefix(path, "/internal/v1/")
}
