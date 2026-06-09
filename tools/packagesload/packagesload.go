// Package packagesload is the single sanctioned entry point for
// golang.org/x/tools/go/packages.Load across GoCell tooling. It forces every
// caller to declare the workspace [Mode] explicitly, so no loader silently
// inherits the ambient GOWORK from the committed repo-root go.work.
//
// WHY: PR #1554 commits a go.work (`use .`), putting `go` into workspace mode
// repo-wide. Most loaders analyze either the root module or an isolated
// standalone module (the archtest / depgraph fixtures under testdata/*, or a
// release-pinned per-module build) and MUST use [ModeModule] (GOWORK=off) —
// workspace mode otherwise rejects any directory whose module is not in the
// `use` set ("directory ... does not contain modules listed in go.work"). A
// future cross-module archtest loader that genuinely wants to see every member
// module's AST through go.work (Plan D §"Cross-Module archtest") uses
// [ModeWorkspace]. Encoding the choice as a mandatory typed parameter — rather
// than an ad-hoc GOWORK=off ban on every packages.Config — keeps both intents
// expressible and prevents silent ambient inheritance.
//
// Enforced by archtest PACKAGES-LOAD-FUNNEL-01: packages.Load may be called
// directly ONLY from this package.
package packagesload

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Mode selects the module-resolution semantics for a load.
type Mode int

const (
	// ModeModule forces module mode by appending GOWORK=off to the environment:
	// the load resolves against the target directory's own go.mod, ignoring any
	// repo-root go.work. Required for loading standalone modules not listed in
	// the workspace `use` set (archtest / depgraph testdata fixtures) and for
	// release-pinned per-module builds.
	ModeModule Mode = iota
	// ModeWorkspace uses the target workspace: the load resolves across all
	// `use` member modules. For loaders that genuinely need a cross-module view.
	// If the process has GOWORK=off but cfg.Dir points at a directory containing
	// go.work, [applyMode] pins that exact go.work path; otherwise GOWORK=off is
	// rejected fail-closed so the scan cannot silently degrade to one module.
	ModeWorkspace
)

// Load runs packages.Load with the GOWORK semantics dictated by mode. The caller
// supplies cfg (Mode/Dir/Tests/BuildFlags/Context); Load owns cfg.Env, deriving
// GOWORK from mode (any caller-set cfg.Env is preserved, with GOWORK=off appended
// last for [ModeModule] so it wins). This is the only place in the repo permitted
// to call packages.Load directly (archtest PACKAGES-LOAD-FUNNEL-01).
func Load(mode Mode, cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
	if err := applyMode(mode, cfg); err != nil {
		return nil, err
	}
	return packages.Load(cfg, patterns...)
}

// applyMode sets cfg.Env from mode (extracted for unit testing without a real
// packages.Load).
func applyMode(mode Mode, cfg *packages.Config) error {
	if cfg == nil {
		return fmt.Errorf("packagesload: nil config")
	}
	switch mode {
	case ModeModule:
		// Appended last so it overrides any inherited / caller-set GOWORK.
		cfg.Env = append(baseEnv(cfg.Env), "GOWORK=off")
	case ModeWorkspace:
		// Inherit the ambient workspace. If a module-level test has forced
		// GOWORK=off but the caller supplied the workspace root as cfg.Dir, pin
		// that exact go.work path so cross-module scans stay deterministic.
		env := baseEnv(cfg.Env)
		if v, ok := effectiveGOWORK(env); ok && v == "off" {
			goWork, err := workspaceFileForDir(cfg.Dir)
			if err != nil {
				return err
			}
			env = append(withoutGOWORK(env), "GOWORK="+goWork)
		}
		cfg.Env = env
	default:
		return fmt.Errorf("packagesload: invalid mode %d", mode)
	}
	return nil
}

// baseEnv returns the caller-supplied environment, or the process environment
// when the caller left cfg.Env nil (packages.Load's own default).
func baseEnv(cfgEnv []string) []string {
	if cfgEnv == nil {
		return os.Environ()
	}
	return cfgEnv
}

func workspaceFileForDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf(
			"packagesload: ModeWorkspace requires an active go.work workspace, but GOWORK=off is set and cfg.Dir is empty; " +
				"unset GOWORK or set cfg.Dir to the target workspace root",
		)
	}
	goWork := filepath.Join(dir, "go.work")
	if _, err := os.Stat(goWork); err != nil {
		return "", fmt.Errorf(
			"packagesload: ModeWorkspace requires an active go.work workspace, but GOWORK=off is set and %s is not readable: %w",
			goWork, err,
		)
	}
	abs, err := filepath.Abs(goWork)
	if err != nil {
		return "", fmt.Errorf("packagesload: resolve go.work path %s: %w", goWork, err)
	}
	return abs, nil
}

func withoutGOWORK(env []string) []string {
	out := env[:0]
	for _, e := range env {
		if strings.HasPrefix(e, "GOWORK=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// effectiveGOWORK returns the value of the LAST GOWORK= entry in env (later
// entries override earlier ones, matching exec semantics), and whether any was
// present. Used by [applyMode] to detect GOWORK=off under ModeWorkspace.
func effectiveGOWORK(env []string) (string, bool) {
	val, found := "", false
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GOWORK="); ok {
			val, found = v, true
		}
	}
	return val, found
}
