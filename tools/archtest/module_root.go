package archtest

import (
	"testing"

	"github.com/ghbvf/gocell/tools/workspace"
)

// frameworkSubdir is the workspace-root-relative directory holding the core
// framework module (kernel/runtime/pkg) since the #1565 split. It is an on-disk
// dir name, NOT a module path, so it is not a platform-path literal under
// ARCHTEST-MODULE-PATH-FUNNEL-01. Used by the anchor tests that read
// framework/go.mod directly (external_test.go, root_module_no_replace_test.go).
const frameworkSubdir = "framework"

// lookupModuleRoot returns the scan root by walking up from the process's cwd.
// It delegates to [workspace.WorkspaceRoot] (go.work-first, single-module
// go.mod fallback) and returns an error rather than calling t.Fatalf, so it is
// safe to call from non-test contexts such as TestMain.
//
// go.work-first means the GoCell monorepo always anchors to the WORKSPACE root
// — never to a nested module's go.mod when archtest runs from a subdirectory,
// which would drop every other workspace module from the production scan. The
// single-module go.mod fallback fires only where there is no go.work, i.e. an
// external single-module consumer repo (Operator-SDK, #1081) running
// RunStandardCellRules against its own module. Isolated fixture modules
// (StandaloneModule) carry their own go.mod and are loaded by their explicit
// dir — they never reach this walk.
func lookupModuleRoot() (string, error) {
	return workspace.WorkspaceRoot()
}

// findModuleRoot is a thin wrapper around [lookupModuleRoot] that converts
// errors to t.Fatalf. It is the single workspace-root discovery source shared by
// [Run] (pass.go) and the go-list integration helpers in archtest_test.go.
//
// The parameter is [testing.TB] (not [*testing.T]) so the unified [Run] driver
// — which accepts testing.TB to support the StandaloneModule fatal-path spy
// tests — can resolve the workspace root for its Typed / Production / Fixture
// scopes through this single source.
//
// Failure modes (getwd error, no go.work above cwd) terminate the test via
// t.Fatalf. archtest drivers are unconditionally fail-loud.
func findModuleRoot(t testing.TB) string {
	t.Helper()
	root, err := lookupModuleRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return root
}

// moduleImportPath returns the GoCell org/repo PREFIX (e.g. "github.com/ghbvf/gocell")
// for the workspace anchored at root. Callers compose sibling-module symbol paths
// as <prefix>+"/adapters/…", "/tools/…", "/corecells", and framework-internal
// symbol paths as <prefix>+"/framework/kernel/…" (post-#1565 split). The set of
// ALL workspace modules (production scan / classification) comes from
// [findWorkspaceModules], not this function.
//
// Resolution (never hardcoded, so a module rename / /v2 bump is caught by
// TestPlatformModulePathMatchesGoMod):
//   - external single-module consumer (Operator-SDK, #1081): go.mod sits at root
//     → return its declared module path verbatim.
//   - GoCell workspace (#1565): the workspace root holds NO go.mod (only go.work);
//     the core framework module lives at root/framework. Read framework/go.mod and
//     strip the trailing "/framework" to recover the org prefix the siblings share.
//
// The derivation is single-sourced in [workspace.CorePrefix] (shared with
// modrelease / releasesmoke), which reads framework/go.mod and strips "/framework".
func moduleImportPath(root string) (string, error) {
	return workspace.CorePrefix(root)
}

// findWorkspaceModules returns every workspace member declared in root/go.work
// (cross-checked against .gocell/manifest.yaml), in go.work `use` order, each
// carrying its on-disk Dir and Go ImportPath. It is the production-scan /
// classification module set for the [Production] scope and the depgraph LAYER
// rules — derived from go.work so an extracted nested module is auto-covered
// (the keystone of #1555). It is the t.Fatalf wrapper around
// [workspace.Modules]; TestMain (non-test context) calls workspace.Modules
// directly.
func findWorkspaceModules(t testing.TB, root string) []workspace.Module {
	t.Helper()
	mods, err := workspace.Modules(root)
	if err != nil {
		t.Fatalf("archtest.Run: workspace module set: %v", err)
	}
	return mods
}

// moduleImportPaths projects the Go import paths out of a workspace module set,
// for callers that classify by import path (depgraph FromPackages / Classifier).
func moduleImportPaths(mods []workspace.Module) []string {
	paths := make([]string, len(mods))
	for i, m := range mods {
		paths[i] = m.ImportPath
	}
	return paths
}
