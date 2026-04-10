// Package verify provides metadata-driven verification runners for cells,
// slices, and journeys. It shells out to "go test" and uses the verify
// blocks declared in cell.yaml, slice.yaml, and journey YAML files to
// determine which tests to execute.
package verify

import "strings"

// kebabToCamelCase converts a kebab-case string to CamelCase.
// Each hyphen-separated segment is title-cased.
// Dots are preserved as separators; each dot-segment is independently converted.
//
// Examples:
//
//	"session-revoke"  → "SessionRevoke"
//	"startup"         → "Startup"
//	"oidc-redirect"   → "OidcRedirect"
//	""                → ""
func kebabToCamelCase(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, seg := range strings.Split(s, "-") {
		if seg == "" {
			continue
		}
		b.WriteString(strings.ToUpper(seg[:1]))
		b.WriteString(seg[1:])
	}
	return b.String()
}
