package typeseval

import "go/build"

// BuildContextPredicate returns a tag predicate suitable for
// constraint.Expr.Eval. It returns true for any tag the Go toolchain sets
// implicitly under a standard CI context plus any extraTags supplied by
// the caller.
//
// Implicit defaults (union over all GOOS/GOARCH — fail-closed
// over-approximation: a constraint gated on "linux" is treated as
// satisfiable by some CI context):
//   - GOOS values (all known)
//   - GOARCH values (all known)
//   - "cgo" (CGO_ENABLED=1 is the toolchain default)
//   - "unix" (alias active for unix-family GOOS)
//   - "gc" (the standard compiler tag)
//   - go1.X release tags (sourced from build.Default.ReleaseTags so
//     toolchain upgrades automatically refresh the set)
//
// Repo-private skip markers (catalog_gen, never) are NOT implicit defaults
// and must not be returned true by this predicate. Knowledge that those
// tags exist lives in buildtags_test.go::repoSkipTagAllowlist for the
// coverage self-test only.
//
// The implicit defaults map is INTENTIONALLY UNEXPORTED. Forcing every
// consumer through this constructor ensures that future additions to the
// default set (e.g. a new release tag after a go.mod floor bump, or a new
// implicit toolchain tag) automatically reach all archtest predicates
// without hand-edit drift. A caller that hand-rolled
// `expr.Eval(func(t string) bool { return myMap[t] })` would silently
// miss those additions; that error is unavailable here by API design.
//
// ref: golang/go src/go/build/build.go Default.ReleaseTags
// ref: internal/syslist (canonical GOOS/GOARCH; mirrored here because the
// stdlib does not export the lists).
func BuildContextPredicate(extraTags ...string) func(tag string) bool {
	extra := make(map[string]bool, len(extraTags))
	for _, t := range extraTags {
		extra[t] = true
	}
	return func(tag string) bool {
		return implicitDefaults[tag] || extra[tag]
	}
}

// implicitDefaults is the union of toolchain-default build tags. Built
// once at package init from build.Default.ReleaseTags plus hardcoded
// GOOS/GOARCH/cgo/unix/gc sets. UNEXPORTED ON PURPOSE — see
// BuildContextPredicate godoc.
var implicitDefaults = buildImplicitDefaults()

func buildImplicitDefaults() map[string]bool {
	out := map[string]bool{
		"cgo":  true,
		"unix": true, // alias for unix-family GOOS — true under all unix runners
		"gc":   true, // standard compiler tag
	}
	for _, goos := range knownGOOS {
		out[goos] = true
	}
	for _, goarch := range knownGOARCH {
		out[goarch] = true
	}
	for _, releaseTag := range build.Default.ReleaseTags {
		out[releaseTag] = true
	}
	return out
}

// knownGOOS / knownGOARCH mirror internal/syslist (Go stdlib does not export
// these lists). Update when Go adds a new platform — uncommon (years).
var knownGOOS = []string{
	"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos",
	"ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris",
	"wasip1", "windows", "zos",
}

var knownGOARCH = []string{
	"386", "amd64", "amd64p32", "arm", "arm64", "arm64be", "armbe",
	"loong64", "mips", "mips64", "mips64le", "mips64p32", "mips64p32le",
	"mipsle", "ppc", "ppc64", "ppc64le", "riscv", "riscv64", "s390",
	"s390x", "sparc", "sparc64", "wasm",
}
