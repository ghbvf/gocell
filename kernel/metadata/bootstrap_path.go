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

// IsPublicHTTPPath reports whether path targets the public-facing HTTP
// listener (the internet-exposed API surface). The classification covers all
// versioned public API prefixes — /api/vN — without locking to a specific
// version number, because the trust-boundary concern (public internet vs.
// internal control-plane) is version-agnostic: any path under /api/* is
// reachable by external principals and subject to the public-listener auth
// chain.
//
// Contrast with IsInternalHTTPPath, which is locked to /internal/v1 because
// internal path versioning is tightly controlled and version-specific router
// wiring matters. The public oracle is deliberately version-agnostic.
//
// Accepted:
//
//	/api
//	/api/v1/config/{key}
//	/api/v2/users
//
// Rejected (fail-closed):
//
//	""                     (empty string)
//	"/apix/v1/foo"        (prefix must be /api/ or exactly /api, not /apix)
//	"/internal/v1/foo"    (internal listener, not public)
//	"/healthz"            (framework probe, no trust-boundary flag)
//
// ref: FMT-33 SLICE-HTTP-VISIBILITY-SEGREGATION-01; symmetric counterpart to
// IsInternalHTTPPath for the public trust boundary.
func IsPublicHTTPPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}
