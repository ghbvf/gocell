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
	// ModeWorkspace uses the ambient workspace (the repo-root go.work): the load
	// resolves across all `use` member modules. For loaders that genuinely need
	// a cross-module view. GOWORK=off is rejected fail-closed here — under it the
	// `go` tool silently falls back to a single-module context, dropping every
	// non-core member from the scan (the exact silent-coverage-loss #1555
	// prevents). See [applyMode].
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
		// Inherit the ambient workspace; leave GOWORK untouched — UNLESS it is
		// explicitly off, in which case `go` would silently degrade to a
		// single-module context and drop every non-core workspace member from
		// the scan. That is the exact silent-coverage-loss #1555 guards against,
		// so reject it fail-closed rather than producing a partial graph.
		env := baseEnv(cfg.Env)
		if v, ok := effectiveGOWORK(env); ok && v == "off" {
			return fmt.Errorf(
				"packagesload: ModeWorkspace requires an active go.work workspace, but GOWORK=off is set; " +
					"unset GOWORK (or point it at the target go.work) so the cross-module scan covers every member",
			)
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

// effectiveGOWORK returns the value of the LAST GOWORK= entry in env (later
// entries override earlier ones, matching exec semantics), and whether any was
// present. Used by [applyMode] to reject GOWORK=off under ModeWorkspace.
func effectiveGOWORK(env []string) (string, bool) {
	val, found := "", false
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GOWORK="); ok {
			val, found = v, true
		}
	}
	return val, found
}
