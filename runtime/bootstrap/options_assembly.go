package bootstrap

// options_assembly.go — With* option functions covering config loading and
// CoreAssembly construction.
//
// Covers: WithConfig, WithAssembly, WithAssemblyID, WithClock.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.

import (
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/clock"
)

// WithClock sets the single root [clock.Clock] threaded through the
// bootstrap, default assembly, lifecycle, and (via Dependencies) every
// registered cell. It is required: bootstrap.New panics when WithClock is
// not applied. Production callers pass clock.Real(); tests pass
// clockmock.New(...).
//
// When WithAssembly is also used, the same clock instance must be passed
// to both bootstrap.WithClock and assembly.New(Config{Clock: ...}).
// Bootstrap.Run performs a phase0 fail-fast check and returns an error
// when the two instances differ.
func WithClock(clk clock.Clock) Option {
	return func(b *Bootstrap) {
		b.clock = clk
	}
}

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
