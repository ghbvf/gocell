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
		{"integration", "mqtt_tls"},
		{"integration_cluster"},
		// archtest gates the archtest leaf suite itself (every
		// tools/archtest/*_test.go carries //go:build archtest —
		// ARCHTEST-LEAF-BUILD-TAG-01 — to keep the ~1200-test suite off the bare
		// `go test ./...` execution path; see ADR 202605120000 §Amendment
		// 2026-06-10). Unlike archtest_fixture below, archtest gates REAL rule
		// files (not RED fixtures): the meta-scanners that load PatternsExtended
		// (which spans ./tools/...) under FlatNonDefaultTags MUST still see those
		// rule files (TEST-TIME-LITERAL / TEST-SLEEP-DISCIPLINE / MODULE-PATH-FUNNEL
		// etc. apply to them), exactly as they did before the suite was tagged.
		{"archtest"},
		// archtest_fixture is deliberately ABSENT (#944): the fixture build tag
		// is NOT a generic production tag and must never enter this union, or a
		// business Run(t, Typed(TypedOpts{Tags: FlatNonDefaultTags()}, ...)) scan
		// would load fixture-tagged code, bypassing the Fixture-scope funnel. The
		// tag is a build-mode skip marker recognized by the coverage self-test via
		// repoSkipTagAllowlist (buildtags_test.go), alongside catalog_gen/never.
		// The only sanctioned fixture loader is Run(t, Fixture(...)), whose
		// body injects the literal internally.
	}
}

// catalog_gen 不在 KnownNonDefaultTags 中：cmd/corebundle/catalog_gen_stub.go 用
// //go:build !catalog_gen 守护，活跃版本是 stub（默认 build 即包含），真正的
// catalog_gen.go 是 .gitignore'd 的 codegen 产物，只在 CI -tags=catalog_gen 模式
// 下存在。把 catalog_gen 加进 KnownNonDefaultTags 会让 LoadPackages 在 clean tree
// 上尝试加载不存在的 generated 文件 → undefined symbol。该 tag 在
// buildtags_test.go::platformTagAllowlist 与 "never" synthetic skip-marker
// 同类声明为 build-mode skip，仅用于覆盖自检知晓存在性。
//
// AST-aware extractor（COMMENTGROUP-COVERAGE-01）首次 surface 了这个 tag——
// 旧 bufio.Scanner 因 file doc comment 与 build directive 间空行误判而静默漏掉
// （Soft → Medium 升级的真实 dividend）。

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
