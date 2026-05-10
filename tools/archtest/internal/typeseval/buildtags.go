package typeseval

import "sort"

// KnownNonDefaultTags returns the build tag combinations that gate test or
// production files in this repo. archtest rules that must scan every
// tag-set call SharedResolver once per group and dedupe diagnostics by
// (rel, line, message).
//
// Single source: this list is the authoritative set. Whenever a new build
// tag is introduced anywhere under the module, add the corresponding
// combination here AND let TestKnownNonDefaultTagsCoverage in
// buildtags_test.go catch the gap (fail-closed: any //go:build directive
// referencing a tag not represented here causes the self-test to fail).
//
// Each entry is a `tags` slice as accepted by LoadPackages /
// SharedResolver — empty (nil) means the default build tag set;
// {"e2e", "pg"} means both tags must be active for the targeted files
// to be loaded.
//
// Closes PR445-FU finding F2 + the file-local testTimeLiteralBuildTags
// constant in test_time_literal_test.go (cross-rule single source).
func KnownNonDefaultTags() [][]string {
	return [][]string{
		nil, // default build (no extra tags)
		{"integration"},
		{"e2e"},
		{"e2e", "pg"},
		{"examples_smoke"},
		{"integration", "otelcollector"},
		{"integration_cluster"},
	}
}

// FlatNonDefaultTags returns the union of all distinct non-empty tags
// appearing in KnownNonDefaultTags(), sorted. Suitable for callers that
// need a single LoadPackages call carrying every tag at once (e.g.
// test_time_literal_test.go's universal AST walk). Excludes nil.
func FlatNonDefaultTags() []string {
	seen := map[string]struct{}{}
	for _, group := range KnownNonDefaultTags() {
		for _, tag := range group {
			seen[tag] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}
