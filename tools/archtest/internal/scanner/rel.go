package scanner

import "strings"

// frameworkRelPrefix is the physical path segment under which the GoCell core
// framework module's kernel/runtime/pkg live since the #1565 split.
const frameworkRelPrefix = "framework/"

// StripFrameworkPrefix maps a modRoot-relative path to its LOGICAL layer path by
// dropping the physical "framework/" module wrapper, so a framework file reports
// as "kernel/outbox/result.go" rather than "framework/kernel/outbox/result.go".
//
// This mirrors the identical normalization the typeseval Pass.Rel driver applies
// (tools/archtest/pass.go stripFrameworkPrefix): archtest rules identify
// sanctioned sites by their position in GoCell's LOGICAL layer layout (kernel/
// runtime/ pkg/ adapters/ corecells/ …), which the #1565 move did NOT change —
// only the physical module nesting did. Reporting the logical path keeps every
// rule's path-identity (file allowlists, MatchRels/ExcludeRels predicates, layer
// checks) stable across the reorganization. Only files physically under
// <modRoot>/framework/ carry this prefix (kernel/runtime/pkg exist nowhere else
// at the workspace root), so the strip is unambiguous and collision-free.
// DISK-scanning inputs (DirsScope dirs) keep the PHYSICAL "framework/…" path to
// locate files; this normalization applies only to the REPORTED rel identity.
func StripFrameworkPrefix(rel string) string {
	return strings.TrimPrefix(rel, frameworkRelPrefix)
}
