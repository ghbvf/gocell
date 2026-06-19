package archtest

import "testing"

// RuntimeScopeConfig is the resolved, per-run scope context that bundles the
// workspace root, target module path, framework/platform module paths, and
// platform-cell scan directories into a single sealed value.
//
// # Motivation and single-source guarantee
//
// Before #2329 these four values were resolved independently at each call site
// (lookupModuleRoot, moduleImportPath, PlatformFrameworkModulePath, PlatformCellsDir)
// and passed as separate arguments to runTypedWithRoot / runProduction /
// platformCellScanDirs — making it easy for a site to diverge or forget one
// of the values. RuntimeScopeConfig is the single construction point: callers
// obtain it via [DefaultScopeConfig] or [defaultScopeConfig], and the defaults-
// lock test (TestDefaultScopeConfig_ReproducesGoCellDefaults) pins each field
// to its pre-#2329 value so behavior is byte-identical.
//
// # Sealed construction
//
// All fields are unexported and the struct embeds the unexported [scopeConfigSeal]
// marker field, which makes composite-literal construction outside this package a
// compile error — the only legitimate mint site is the constructors in this file
// (a Hard governance property per the ai-robust framework).
//
// # Relation to ConfigForExternalCell and future issues
//
// [ConfigForExternalCell] (external.go) is the consumer-supplied description of
// the module to analyze — the "derive-not-supply" side. RuntimeScopeConfig is the
// INTERNAL, fully-resolved runtime counterpart assembled from go.work / go.mod
// discovery. External consumers never construct RuntimeScopeConfig directly; the
// drivers call [defaultScopeConfig] on their behalf.
//
// The future Hard-ening funnel (forcing every helper and rule driver through this
// config instead of calling [lookupModuleRoot] / [moduleImportPath] directly) is
// tracked in #2333. The per-runner Scope/WorkspaceRoot wiring in the subprocess
// runner ([cmd/gocell/internal/archtestrunner.Request]) is tracked in #2331.
type RuntimeScopeConfig struct {
	workspaceRoot           string
	targetModulePath        string
	frameworkModulePath     string
	platformModulePath      string
	platformCellsModulePath string
	platformCellScanDirs    []string
	_                       scopeConfigSeal
}

// scopeConfigSeal is the unexported marker that prevents package-external
// composite-literal construction of [RuntimeScopeConfig]. Any attempt to
// write archtest.RuntimeScopeConfig{…} outside this package produces a
// compile error because the blank identifier field _ cannot be named.
type scopeConfigSeal struct{}

// WorkspaceRoot returns the resolved go.work workspace root (or the module
// root for a single-module consumer repo). This is the same value returned by
// [lookupModuleRoot] and used as the root argument to [runTypedWithRoot].
func (c RuntimeScopeConfig) WorkspaceRoot() string { return c.workspaceRoot }

// TargetModulePath returns the Go module import-path prefix of the module
// under analysis (e.g. "github.com/ghbvf/gocell" in GoCell's own dogfood).
// In GoCell this equals [PlatformModulePath]; in an external consumer repo it
// equals the consumer's own module path.
func (c RuntimeScopeConfig) TargetModulePath() string { return c.targetModulePath }

// FrameworkModulePath returns the Go module path of the GoCell framework
// module (kernel/runtime/pkg). Derived from [PlatformFrameworkModulePath].
func (c RuntimeScopeConfig) FrameworkModulePath() string { return c.frameworkModulePath }

// PlatformModulePath returns the GoCell org/repo prefix shared by every GoCell
// module. Equals [PlatformModulePath].
func (c RuntimeScopeConfig) PlatformModulePath() string { return c.platformModulePath }

// PlatformCellsModulePath returns the Go module path of GoCell's platform-cells
// module. Equals [PlatformCellsModulePath].
func (c RuntimeScopeConfig) PlatformCellsModulePath() string { return c.platformCellsModulePath }

// PlatformCellScanDirs returns a defensive copy of the repo-relative on-disk
// scan roots that hold GoCell's own platform cells. The source is
// [platformCellScanDirsDefault], shared with [platformCellScanDirs], so both
// callers agree on the exact set (#2329 single-source requirement).
func (c RuntimeScopeConfig) PlatformCellScanDirs() []string {
	out := make([]string, len(c.platformCellScanDirs))
	copy(out, c.platformCellScanDirs)
	return out
}

// DefaultScopeConfig constructs a RuntimeScopeConfig from the running process's
// go.work / go.mod environment. It resolves the workspace root via
// [lookupModuleRoot] and the target module path via [moduleImportPath], then
// assembles the remaining fields from the Platform* consts so that all paths
// derive from [PlatformModulePath] (ARCHTEST-MODULE-PATH-FUNNEL-01).
//
// Returns a non-nil error if workspace root discovery or module-path derivation
// fails (e.g. no go.work above the cwd and no go.mod found). On error the
// returned RuntimeScopeConfig is the zero value and must not be used.
func DefaultScopeConfig() (RuntimeScopeConfig, error) {
	root, err := lookupModuleRoot()
	if err != nil {
		return RuntimeScopeConfig{}, err
	}
	target, err := moduleImportPath(root)
	if err != nil {
		return RuntimeScopeConfig{}, err
	}
	return RuntimeScopeConfig{
		workspaceRoot:           root,
		targetModulePath:        target,
		frameworkModulePath:     PlatformFrameworkModulePath,
		platformModulePath:      PlatformModulePath,
		platformCellsModulePath: PlatformCellsModulePath,
		platformCellScanDirs:    platformCellScanDirsDefault(),
	}, nil
}

// defaultScopeConfig is the t.Fatalf wrapper around [DefaultScopeConfig] for
// use inside test drivers that want fail-loud semantics (the same pattern as
// [findModuleRoot] wrapping [lookupModuleRoot]).
func defaultScopeConfig(t testing.TB) RuntimeScopeConfig {
	t.Helper()
	cfg, err := DefaultScopeConfig()
	if err != nil {
		t.Fatalf("archtest: defaultScopeConfig: %v", err)
	}
	return cfg
}
