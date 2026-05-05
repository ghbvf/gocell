// Package pathx provides shared path-conversion helpers for codegen tooling.
package pathx

import "strings"

// ContractIDToPackagePath converts a contract ID to the module-relative
// generated package path.
//
//   - "event.session.created.v1"         → "generated/contracts/event/session/created/v1"
//   - "http.config.internal.get.v1"      → "generated/contracts/http/config/internalapi/get/v1"
//   - "http.internal.devicecommands.v1"  → "generated/contracts/http/internalapi/devicecommands/v1"
//
// Every "internal" segment in the contract ID is rewritten to "internalapi"
// so that generated packages under http/internal/... remain importable from
// cells/ and examples/ — Go's internal package rule would otherwise block
// cross-directory imports.  Contract IDs (http.internal.X.v1) and URL prefixes
// (/internal/v1/...) are unchanged; only the generated filesystem path segment
// is renamed.
//
// Single source of truth for contract ID → generated package path mapping.
// Importers: contractgen, cellgen, archtest.
//
// ref: golang/go internal package rule (https://go.dev/ref/spec#Internal_packages)
func ContractIDToPackagePath(id string) string {
	parts := strings.Split(id, ".")
	segments := make([]string, len(parts))
	for i, p := range parts {
		if p == "internal" {
			segments[i] = "internalapi"
		} else {
			segments[i] = p
		}
	}
	return "generated/contracts/" + strings.Join(segments, "/")
}
