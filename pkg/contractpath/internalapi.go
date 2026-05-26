// Package contractpath provides the single source of truth for converting a
// contract ID to its module-relative generated package path. Importers:
// kernel/governance (CH-04/05/06 generated artifact lookup), tools/codegen
// (contractgen, cellgen), and archtest.
package contractpath

import "strings"

// Segments returns the path segments for a contract ID's generated package,
// with each "internal" segment rewritten to "internalapi" so packages under
// http/internal/... stay importable from cells/ and examples/ (Go's internal
// package rule would otherwise block cross-directory imports). Callers that
// need the joined "generated/contracts/..." string use
// ContractIDToPackagePath; callers that join path segments themselves (e.g.
// kernel/governance via safeJoinUnderRoot for path-traversal protection) use
// Segments directly.
//
// Contract IDs (http.internal.X.v1) and URL prefixes (/internal/v1/...) are
// unchanged; only the generated filesystem path segment is renamed.
//
// ref: golang/go internal package rule (https://go.dev/ref/spec#Internal_packages)
func Segments(id string) []string {
	parts := strings.Split(id, ".")
	segments := make([]string, len(parts))
	for i, p := range parts {
		if p == "internal" {
			segments[i] = "internalapi"
		} else {
			segments[i] = p
		}
	}
	return segments
}

// ContractIDToPackagePath converts a contract ID to the module-relative
// generated package path.
//
//   - "event.session.created.v1"         → "generated/contracts/event/session/created/v1"
//   - "http.config.internal.get.v1"      → "generated/contracts/http/config/internalapi/get/v1"
//   - "http.internal.devicecommands.v1"  → "generated/contracts/http/internalapi/devicecommands/v1"
//
// See Segments for the rewrite rationale.
func ContractIDToPackagePath(id string) string {
	return "generated/contracts/" + strings.Join(Segments(id), "/")
}
