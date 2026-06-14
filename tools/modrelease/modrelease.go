// Package modrelease is the synchronized multi-module release tool for the
// GoCell workspace. It rewrites the internal `require` versions of every
// publishable LIBRARY module to a single release version and derives the
// per-module git tag set, so satellites become externally consumable via
// `go get <module>@vX.Y.Z`.
//
// # Why
//
// Each satellite go.mod declares its intra-repo dependencies as
// `require github.com/ghbvf/gocell[/path] v0.0.0` + a local
// `replace … => ../relative`. `v0.0.0` is an unpublished placeholder: an external
// consumer's `go get github.com/ghbvf/gocell/adapters/postgres@<tag>` cannot
// resolve it. The release rewrites every internal require to the real release
// version (v0.0.0 / pseudo / a prior real version → vX.Y.Z) and tags each module
// `<reldir>/vX.Y.Z`.
//
// # Keep replace (NOT stripped)
//
// The local `replace` directives are LEFT UNTOUCHED for LIBRARY modules, matching
// the OpenTelemetry-Go published shape (ref: open-telemetry/opentelemetry-go
// exporters/otlp/otlptrace/go.mod@exporters/otlp/otlptrace/v1.24.0 — replace kept
// at the release tag). Go IGNORES a dependency module's replace directives, so a
// published library carrying `replace … => ../local` is harmless to consumers
// (they resolve the bumped `require … vX.Y.Z` from the sibling's own tag), while
// the same replace keeps `GOWORK=off go list -m all` (hack/verify-workspace.sh)
// resolving unpublished siblings on the develop branch. The version-only rewrite
// (ref: open-telemetry/opentelemetry-go-build-tools multimod replaceModVersion)
// touches require versions only; replace lines carry no version and are never
// matched.
//
// # Scope: library modules only (publishable set) + installable binaries
//
// The publishable library set is every go.work member EXCEPT examples/*, tests/*,
// and cmd/* (see [IsPublishable]). The publishable set is handled by [BumpTree] and
// [TagPaths] which preserve replace directives (OTel-canonical shape).
//
// # Installable binaries
//
// cmd/* binaries in [installableBinaries] (currently "cmd/gocell") need a
// different release-time treatment: `go install pkg@version` is rejected outright
// when the installed module's go.mod contains a replace directive. The solution
// is a release-time strip+pin+separate-tag pipeline:
//   - [stripAndPinBytes] strips ALL replace directives and pins every internal
//     require to the release version (pure function, no disk I/O).
//   - [StripReplaceAndPin] wraps stripAndPinBytes with disk read+write for use in
//     the release workflow.
//   - [InstallableTagPaths] derives the per-binary git tag set (separate from the
//     library tag set from [TagPaths]).
//
// The develop branch cmd/gocell/go.mod keeps its replace directives untouched
// (multiple GOWORK=off scripts depend on them). Only the release workflow calls
// StripReplaceAndPin to produce the tagged installable tree. This mechanism
// resolves #2045.
package modrelease

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/ghbvf/gocell/tools/workspace"
)

// Result reports the require-version rewrites a single [BumpModule] applied.
type Result struct {
	// Dir is the module directory as passed to BumpModule (or the go.work
	// use-dir when produced by BumpTree).
	Dir string
	// Requires lists the internal module paths whose require version was
	// rewritten, in file order. Empty when the module was already at the target
	// version or carries no internal require.
	Requires []string
}

// IsPublishable reports whether the workspace member at reldir (a go.work `use`
// directory, filepath.Clean'd, "." for the root module) belongs to the
// externally-published LIBRARY set.
//
// Deny predicate: examples/* are leaf apps; cmd/* are `go install` binaries /
// deployment artifacts whose go-install-at-version path conflicts with the kept
// replace directives (#1088); the tests/ SUB-modules (tests/integration suites,
// tests/testutil/pgshare) are leaf consumers. The `tests` module itself is NOT a
// leaf: it ships shared test helpers (tests/testutil/pgclone) that a publishable
// adapter depends on in non-test code (adapters/postgres/pgtest), so it must
// publish for the adapter to stay externally consumable (#1565 — pre-split these
// helpers lived in the published root module). Everything else publishes by
// default — a new adapters/foo is publishable (fail-toward-publish), a new cmd/foo
// binary is excluded (fail-toward-exclude-binary). This predicate is the single
// source of the allow/deny rule; the set itself is DERIVED from go.work (see
// [PublishableModules]), never a hand-maintained list.
func IsPublishable(reldir string) bool {
	slash := filepath.ToSlash(filepath.Clean(reldir))
	for _, deny := range [...]string{"examples", "cmd"} {
		if slash == deny || strings.HasPrefix(slash, deny+"/") {
			return false
		}
	}
	// Deny the tests/ SUB-modules (leaves) but publish the `tests` module itself
	// (a required shared-helper library — see godoc above).
	if strings.HasPrefix(slash, "tests/") {
		return false
	}
	return true
}

// PublishableModules returns the publishable subset of the workspace rooted at
// root, in go.work `use` order. It is the single source shared by the release
// tagger, the golden drift guard, and the external smoke test: the set is
// workspace.Modules(root) filtered by [IsPublishable], so it can never drift from
// the go.work the toolchain actually compiles.
func PublishableModules(root string) ([]workspace.Module, error) {
	mods, err := workspace.Modules(root)
	if err != nil {
		return nil, fmt.Errorf("modrelease: enumerate workspace: %w", err)
	}
	out := make([]workspace.Module, 0, len(mods))
	for _, m := range mods {
		if IsPublishable(m.Dir) {
			out = append(out, m)
		}
	}
	return out, nil
}

// internalRequireRE builds the per-line matcher for an internal require whose
// module path is rooted at prefix. Capture groups:
//
//	1: leading whitespace + optional `require ` keyword (single-line form)
//	2: the internal module path (prefix, optionally with a /subpath)
//	3: whitespace between path and version
//	4: the current version token (vX.Y.Z, pseudo, or v0.0.0)
//	5: the remainder of the line (e.g. " // indirect")
//
// `module`, single-line `replace`/`exclude`/`retract` lines never match: they
// begin with their own keyword, so neither the optional `require ` nor the bare
// path can anchor at line start. The (?:/\S+)? after the quoted prefix is bounded
// by the following \s+, so a sibling namespace like github.com/ghbvf/gocellxyz
// cannot false-match.
//
// A BLOCK-form `exclude (` / `retract (` whose inner line carries an internal path
// (no keyword on that line) would otherwise match this regex; [bumpModuleRE] guards
// it with a small block-context state machine that skips lines inside those blocks
// (see [blockOpens]). require blocks are the only multi-line form the bump targets;
// single-line exclude/retract is keyword-anchored and never matches. Both forms are
// covered by tests.
func internalRequireRE(prefix string) *regexp.Regexp {
	return regexp.MustCompile(
		`^(\s*(?:require\s+)?)(` + regexp.QuoteMeta(prefix) + `(?:/\S+)?)(\s+)(v\S+)(.*)$`,
	)
}

// blockOpens reports whether trimmed (a whitespace-trimmed line) opens a go.mod
// directive block for keyword — i.e. `<keyword> (`, tolerating inner whitespace.
func blockOpens(trimmed []byte, keyword string) bool {
	if !bytes.HasPrefix(trimmed, []byte(keyword)) {
		return false
	}
	rest := bytes.TrimLeft(trimmed[len(keyword):], " \t")
	return string(rest) == "("
}

// validReleaseVersion checks version is a canonical semver tag (e.g. "v1.2.3")
// whose major is v0 or v1. A v2+ major requires the module path to end in /vN
// (Go semantic import versioning), which no gocell module carries yet — so a v2
// release would mint unresolvable adapters/postgres/v2.0.0-style tags. Reject it
// until that path migration lands. ref: https://go.dev/blog/v2-go-modules
func validReleaseVersion(version string) error {
	if !semver.IsValid(version) || semver.Canonical(version) != version {
		return fmt.Errorf("modrelease: version %q is not a canonical semver tag (want e.g. v1.2.3)", version)
	}
	if m := semver.Major(version); m != "v0" && m != "v1" {
		return fmt.Errorf("modrelease: version %q has major %s; gocell modules have no /vN path suffix, "+
			"so v2+ tags would be unresolvable (Go semantic import versioning) — migrate module paths to /vN first", version, m)
	}
	return nil
}

// BumpModule rewrites every internal require version in dir/go.mod (a require
// whose module path is prefix or under prefix/) to version, preserving replace
// directives, `// indirect` markers, external requires, the module line, and all
// other bytes. It is idempotent (a require already at version is left as-is) and
// writes the file only when at least one require changed. version must be a valid
// canonical semver tag (e.g. "v1.2.3").
func BumpModule(dir, prefix, version string) (Result, error) {
	if err := validReleaseVersion(version); err != nil {
		return Result{}, err
	}
	return bumpModuleRE(dir, internalRequireRE(prefix), version)
}

// bumpModuleRE is the core rewrite, taking a precompiled matcher so BumpTree
// compiles the prefix regexp once across all modules rather than per module.
func bumpModuleRE(dir string, re *regexp.Regexp, version string) (Result, error) {
	p := filepath.Clean(filepath.Join(dir, "go.mod"))
	data, err := os.ReadFile(p)
	if err != nil {
		return Result{}, fmt.Errorf("modrelease: read go.mod: %w", err)
	}
	out, requires := rewriteRequires(data, re, version)
	if len(requires) == 0 {
		return Result{Dir: dir}, nil // no write: nothing internal to bump
	}
	// 0o600: BumpModule only rewrites EXISTING go.mod files, so the mode arg is
	// inert (os.WriteFile preserves an existing file's permissions); 0o600
	// satisfies gosec G306 without altering the real go.mod mode.
	if err := os.WriteFile(p, out, 0o600); err != nil {
		return Result{}, fmt.Errorf("modrelease: write go.mod: %w", err)
	}
	return Result{Dir: dir, Requires: requires}, nil
}

// rewriteRequires returns go.mod bytes with every internal require version bumped
// to version (and the list of bumped paths). Lines inside an `exclude (` /
// `retract (` block are skipped — an internal-path line there carries a version
// and would otherwise match and corrupt the directive; require blocks ARE
// rewritten (that is the bump target). Single-line exclude/retract is
// keyword-anchored and never matches, so only the block form needs the guard.
func rewriteRequires(data []byte, re *regexp.Regexp, version string) ([]byte, []string) {
	lines := bytes.Split(data, []byte("\n"))
	var requires []string
	inExcludeRetract := false
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		switch {
		case blockOpens(trimmed, "exclude") || blockOpens(trimmed, "retract"):
			inExcludeRetract = true
		case inExcludeRetract:
			if string(trimmed) == ")" {
				inExcludeRetract = false
			}
		default:
			if newLine, path := bumpRequireLine(line, re, version); path != "" {
				lines[i] = newLine
				requires = append(requires, path)
			}
		}
	}
	return bytes.Join(lines, []byte("\n")), requires
}

// bumpRequireLine rewrites a single internal require line's version to version,
// returning the new line and the matched module path. It returns ("", path="")
// when line is not an internal require or is already at version (idempotent).
func bumpRequireLine(line []byte, re *regexp.Regexp, version string) ([]byte, string) {
	m := re.FindSubmatch(line)
	if m == nil {
		return nil, ""
	}
	path, oldVer := string(m[2]), string(m[4])
	if oldVer == version {
		return nil, "" // idempotent
	}
	return []byte(string(m[1]) + path + string(m[3]) + version + string(m[5])), path
}

// BumpTree rewrites the internal require versions of every publishable module
// under root to version, in place. The platform module prefix is read from
// root/go.mod (never a hardcoded literal). Returns one Result per publishable
// module, in go.work order.
func BumpTree(root, version string) ([]Result, error) {
	if err := validReleaseVersion(version); err != nil {
		return nil, err
	}
	prefix, err := workspace.CorePrefix(root)
	if err != nil {
		return nil, fmt.Errorf("modrelease: read root module path: %w", err)
	}
	mods, err := PublishableModules(root)
	if err != nil {
		return nil, err
	}
	re := internalRequireRE(prefix)
	results := make([]Result, 0, len(mods))
	for _, m := range mods {
		res, err := bumpModuleRE(filepath.Join(root, m.Dir), re, version)
		if err != nil {
			return nil, fmt.Errorf("modrelease: bump %q: %w", m.Dir, err)
		}
		res.Dir = m.Dir
		results = append(results, res)
	}
	return results, nil
}

// tagPathFor maps a workspace use-dir to its synchronized git tag: the root
// module ("." ) tags as the bare version (vX.Y.Z); a satellite at reldir tags as
// "<reldir>/vX.Y.Z" (Go's submodule tag convention).
func tagPathFor(reldir, version string) string {
	slash := filepath.ToSlash(filepath.Clean(reldir))
	if slash == "." {
		return version
	}
	return slash + "/" + version
}

// TagPaths returns the git tag for every publishable module at version, in
// go.work order: a module at the repo root → "vX.Y.Z", subdir modules →
// "<reldir>/vX.Y.Z". Post-#1565 there is no root module (the core lives in the
// framework/ submodule), so every tag is the "<reldir>/vX.Y.Z" shape; tagPathFor
// keeps the bare-root case for the general Go multi-module convention. The
// release workflow tags exactly these refs at the bump commit.
func TagPaths(root, version string) ([]string, error) {
	if err := validReleaseVersion(version); err != nil {
		return nil, err
	}
	mods, err := PublishableModules(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, tagPathFor(m.Dir, version))
	}
	return out, nil
}

// installableBinaries is the frozen closed set of workspace use-dirs that are
// go-install-able binaries. It is the single source for [InstallableBinaries]
// and [InstallableTagPaths]. To add a new installable binary, append its
// workspace use-dir here (e.g. "cmd/corebundle") and run `make
// update-modrelease-golden`. Guarded by INSTALLABLE-BINARY-SET-01 (Hard).
//
// cmd/gocell is currently the only go-install-able binary (#2045).
// cmd/corebundle is a runtime composition root not meant for go install.
//
// NOTE: when adding a second installable binary, also update the
// "Release installable CLI module" step in .github/workflows/release.yml —
// that step currently asserts exactly one installable binary and would need
// to be refactored into a loop to handle multiple entries.
var installableBinaries = []string{"cmd/gocell"}

// InstallableBinaries returns the installable-binary subset of the workspace
// rooted at root, in go.work `use` order. It filters workspace.Modules(root) to
// only the use-dirs listed in [installableBinaries]. The result is the single
// source of truth shared by the release tagger and the golden drift guard
// (INSTALLABLE-BINARY-SET-01, Hard).
//
// This set is orthogonal to the publishable library set ([PublishableModules] /
// [IsPublishable]): installable binaries need replace-strip+pin at release time,
// while publishable libraries keep their replace directives (OTel-canonical).
func InstallableBinaries(root string) ([]workspace.Module, error) {
	mods, err := workspace.Modules(root)
	if err != nil {
		return nil, fmt.Errorf("modrelease: enumerate workspace: %w", err)
	}
	// Build allow-set for O(1) lookup.
	allow := make(map[string]bool, len(installableBinaries))
	for _, b := range installableBinaries {
		allow[filepath.ToSlash(filepath.Clean(b))] = true
	}
	out := make([]workspace.Module, 0, len(installableBinaries))
	for _, m := range mods {
		if allow[filepath.ToSlash(filepath.Clean(m.Dir))] {
			out = append(out, m)
		}
	}
	return out, nil
}

// stripAndPinBytes strips ALL replace directives from data (a go.mod file) and
// pins every require whose module path equals prefix or has prefix+"/" as a
// prefix to version. External requires and // indirect markers are preserved.
// The result is formatted by modfile.Format (canonical go.mod format).
//
// version must be a valid canonical semver tag (e.g. "v1.2.3"), validated by
// [validReleaseVersion] before any transformation.
//
// This is a pure function: it performs no disk I/O. Callers that need disk
// read+write should use [StripReplaceAndPin].
func stripAndPinBytes(data []byte, prefix, version string) ([]byte, error) {
	if err := validReleaseVersion(version); err != nil {
		return nil, err
	}

	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return nil, fmt.Errorf("modrelease: parse go.mod: %w", err)
	}

	// Drop ALL replace directives.
	for _, r := range f.Replace {
		if err := f.DropReplace(r.Old.Path, r.Old.Version); err != nil {
			return nil, fmt.Errorf("modrelease: drop replace %q: %w", r.Old.Path, err)
		}
	}

	// Pin every internal require to version.
	for _, req := range f.Require {
		path := req.Mod.Path
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			if err := f.AddRequire(path, version); err != nil {
				return nil, fmt.Errorf("modrelease: pin require %q: %w", path, err)
			}
		}
	}

	f.Cleanup()
	return modfile.Format(f.Syntax), nil
}

// StripResult reports the result of a [StripReplaceAndPin] call.
type StripResult struct {
	// Dir is the module directory as passed to StripReplaceAndPin.
	Dir string
	// Requires lists the internal module paths whose require version was
	// pinned to the release version, in file order.
	Requires []string
}

// StripReplaceAndPin reads dir/go.mod, calls [stripAndPinBytes] to strip all
// replace directives and pin internal requires to version, then writes the
// result back to dir/go.mod (mode 0o600, matching [BumpModule]). Returns a
// [StripResult] with the list of pinned internal require paths.
//
// prefix is the root module import path (e.g. "github.com/ghbvf/gocell");
// version must be a valid canonical semver tag. This function is the disk
// wrapper for the pure [stripAndPinBytes] transform. It does NOT bump library
// modules (use [BumpModule] / [BumpTree] for that); it is ONLY for installable
// binaries that need replace-strip at release time.
func StripReplaceAndPin(dir, prefix, version string) (StripResult, error) {
	p := filepath.Clean(filepath.Join(dir, "go.mod"))
	data, err := os.ReadFile(p)
	if err != nil {
		return StripResult{}, fmt.Errorf("modrelease: read go.mod: %w", err)
	}

	out, err := stripAndPinBytes(data, prefix, version)
	if err != nil {
		return StripResult{}, err
	}

	// Collect pinned internal require paths from the output.
	requires := collectPinnedRequires(out, prefix, version)

	// 0o600: matching BumpModule (existing file, mode inert but gosec-clean).
	if err := os.WriteFile(p, out, 0o600); err != nil {
		return StripResult{}, fmt.Errorf("modrelease: write go.mod: %w", err)
	}
	return StripResult{Dir: dir, Requires: requires}, nil
}

// collectPinnedRequires returns the list of internal module paths (under prefix)
// that appear in data at the given version, in file order. This provides the
// Requires field for [StripResult] analogous to [Result.Requires] in [BumpModule].
func collectPinnedRequires(data []byte, prefix, version string) []string {
	re := internalRequireRE(prefix)
	var out []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		m := re.FindSubmatch(line)
		if m == nil {
			continue
		}
		if string(m[4]) == version {
			out = append(out, string(m[2]))
		}
	}
	return out
}

// InstallableTagPaths returns the git tag for every installable binary at
// version, using the same "<reldir>/vX.Y.Z" convention as [tagPathFor]. The
// result is the set of tags the release workflow must create for the
// stripped+pinned installable tree (separate from the library tags from
// [TagPaths]).
func InstallableTagPaths(root, version string) ([]string, error) {
	if err := validReleaseVersion(version); err != nil {
		return nil, err
	}
	mods, err := InstallableBinaries(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, tagPathFor(m.Dir, version))
	}
	return out, nil
}
