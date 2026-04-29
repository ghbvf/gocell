package bootstrap

// options_assembly.go — With* option functions covering config loading and
// CoreAssembly construction.
//
// Covers: WithConfig, WithAssembly, WithAssemblyID, WithHookTimeout,
// WithHookObserver.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.

import (
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
)

// WithConfig sets the YAML config path and environment prefix.
func WithConfig(yamlPath, envPrefix string) Option {
	return func(b *Bootstrap) {
		b.configPath = yamlPath
		b.envPrefix = envPrefix
	}
}

// WithAssembly sets a pre-built CoreAssembly.
func WithAssembly(asm *assembly.CoreAssembly) Option {
	return func(b *Bootstrap) {
		b.assemblyCore = asm
	}
}

// WithAssemblyID sets only the `cell_id` label used by the auto-wired HTTP
// metrics collector (R2). It does not change assembly identity, cell metadata,
// routing, health, tracing, or non-HTTP metrics.
//
// Recommended to set this matching asm.ID() when using WithAssembly(asm);
// omit to reuse assembly ID (auto-derived). Explicit value overrides
// assembly-derived.
//
// When neither WithAssemblyID nor WithAssembly is used, Bootstrap defaults
// to "default" (the ID of the auto-built assembly).
func WithAssemblyID(id string) Option {
	return func(b *Bootstrap) {
		b.assemblyID = id
	}
}

// WithHookTimeout configures the per-hook deadline for the default
// assembly built when no WithAssembly option is supplied. Zero uses
// assembly.DefaultHookTimeout. Negative values disable per-hook
// timeouts entirely.
//
// When WithAssembly is used, the pre-built assembly's Config.HookTimeout
// takes precedence — this option has no effect. For pre-built assemblies,
// set the value directly on assembly.Config when constructing.
func WithHookTimeout(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.hookTimeout = d
		b.hookTimeoutSet = true
	}
}

// WithHookObserver registers a cell lifecycle hook observer for the
// default assembly built when no WithAssembly option is supplied.
//
// When WithAssembly is used, the pre-built assembly's Config.HookObserver
// takes precedence — this option has no effect. For pre-built assemblies,
// set the observer directly on assembly.Config when constructing.
//
// A nil observer (including a typed nil wrapping a nil concrete pointer)
// is equivalent to not calling this option.
func WithHookObserver(obs cell.LifecycleHookObserver) Option {
	return func(b *Bootstrap) {
		if cell.IsNilHookObserver(obs) {
			return
		}
		b.hookObserver = obs
	}
}
