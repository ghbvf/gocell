// INVARIANT: ROOT-MODULE-NO-REPLACE-01
//
// ROOT-MODULE-NO-REPLACE-01 — the workspace ROOT module (github.com/ghbvf/gocell)
// go.mod must carry NO replace and NO exclude directives, so the published module
// is cleanly consumable by external `go get` and `go install pkg@version`.
//
// # Why
//
// GoCell is a public Go module (issue #1723). Two go.mod directives silently
// break external consumption of the root module, and an AI co-author can
// reintroduce either while debugging locally:
//
//   - replace: `go get`/`go build` of a DOWNSTREAM module ignores the dependency's
//     own replace directives, so a `replace … => ../local` left in the published
//     root means consumers reference code they cannot resolve. And `go install
//     github.com/ghbvf/gocell/<pkg>@version` is rejected OUTRIGHT by the toolchain
//     when the target module's go.mod contains any replace/exclude directive.
//   - exclude: same `go install pkg@version` rejection; also distorts the
//     consumer's module graph in ways the root author never intended.
//
// Satellite modules (cmd/gocell, examples/*) legitimately keep a local
// `replace github.com/ghbvf/gocell => ../../` for monorepo dev — those are OUT OF
// SCOPE here; this invariant constrains ONLY the root module. Their inability to
// be `go install`ed at a version tag is a separate, deliberately-deferred concern
// tracked in #1088 (gocell CLI versioned release).
//
// # Grading: Medium, single axis (NOT a funnel)
//
// This is a single static data assertion ("root go.mod has zero replace/exclude"),
// not a caller-allowlist/sealed-construction funnel, so it carries one rating —
// the two-column downstream/upstream funnel format does not apply (same shape as
// SAGA-CONSTRUCTOR-NIL-GUARD-01).
//
// Medium, not Hard: go.mod is DATA, not Go source — the type system cannot make
// "a go.mod contains a replace directive" unexpressable. The check is a structured
// parse (golang.org/x/mod/modfile via gomodutil.ReadReplaceExclude), not a string
// grep, and it runs machine-checked in CI; that is the ceiling for a data-shape
// invariant. There is NO cheaper Hard-upgrade path (a codegen/release-pipeline
// gate would be heavier and is not warranted), so no Hard-upgrade gh issue is
// opened — the Medium ceiling is permanent for this rule shape.
//
// # Blind spots (out of scope by design — NOT silent gaps)
//
//   - Network reachability of root's dependencies: a root require on a private /
//     unfetchable module would still pass this static check. Catching that needs a
//     clean-room `go get @<tag>` network smoke, which is the DX-5 roadmap item
//     (external-consumer-smoke CI), deliberately deferred — this PR runs that smoke
//     once by hand and records the evidence in the PR body.
//   - Only replace + exclude are checked; `retract` / `go` / `toolchain` directives
//     do not block external consumption, so they are intentionally not asserted.
//   - Satellite-module replaces (cmd/gocell, examples/*) — out of scope (see Why).
//
// # Anti-vacuity + reverse self-check
//
//   - Anti-vacuity: TestRootModuleNoReplace01 asserts the resolved root module path
//     == PlatformModulePath, so a mis-resolved or empty go.mod cannot pass silently.
//   - Reverse self-check (negative control): TestRootModuleNoReplace01_NegativeControl
//     feeds testdata/root_module_replace_fixture/go.mod (which DOES carry a replace
//     AND an exclude) to the same detector and asserts both are flagged — proving
//     the green main assertion is not vacuous.

package archtest

import (
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/tools/gomodutil"
)

const rootModuleNoReplaceRule = "ROOT-MODULE-NO-REPLACE-01"

// TestRootModuleNoReplace01 asserts the workspace root module's go.mod carries no
// replace/exclude directives (external-consumability invariant, #1723).
func TestRootModuleNoReplace01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Anti-vacuity: prove we resolved the REAL root module, not a misresolved /
	// empty go.mod that would trivially have zero replace/exclude.
	mod, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("%s: read root module path (anti-vacuity): %v", rootModuleNoReplaceRule, err)
	}
	if mod != PlatformModulePath {
		t.Fatalf("%s: anti-vacuity failed: resolved root module %q, want %q",
			rootModuleNoReplaceRule, mod, PlatformModulePath)
	}

	replaces, excludes, err := gomodutil.ReadReplaceExclude(root)
	if err != nil {
		t.Fatalf("%s: read root go.mod replace/exclude: %v", rootModuleNoReplaceRule, err)
	}
	if len(replaces) > 0 || len(excludes) > 0 {
		t.Errorf("%s: root module %q go.mod must have NO replace/exclude directives "+
			"(they break external `go get` and `go install pkg@version`); "+
			"found replaces=%v excludes=%v. Satellite modules (cmd/gocell, examples/*) may "+
			"keep local replaces, but the root module must not — remove any replace added "+
			"for local debugging before committing.",
			rootModuleNoReplaceRule, mod, replaces, excludes)
	}
}

// TestRootModuleNoReplace01_NegativeControl is the reverse self-check: it feeds a
// fixture go.mod that DOES carry a replace + an exclude to the same detector and
// asserts both are surfaced. If ReadReplaceExclude silently returned empty, the
// main green assertion above would be vacuous; this test fails first.
func TestRootModuleNoReplace01_NegativeControl(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixture := filepath.Join(root, "tools", "archtest", "testdata", "root_module_replace_fixture")

	replaces, excludes, err := gomodutil.ReadReplaceExclude(fixture)
	if err != nil {
		t.Fatalf("%s: read negative-control fixture go.mod: %v", rootModuleNoReplaceRule, err)
	}
	if len(replaces) == 0 {
		t.Errorf("%s: negative control must expose >=1 replace; detector returned none "+
			"(invariant would be vacuous)", rootModuleNoReplaceRule)
	}
	if len(excludes) == 0 {
		t.Errorf("%s: negative control must expose >=1 exclude; detector returned none "+
			"(invariant would be vacuous)", rootModuleNoReplaceRule)
	}
}
