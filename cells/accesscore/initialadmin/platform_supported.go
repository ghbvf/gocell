//go:build linux || darwin || windows

package initialadmin

// PlatformSupported reports whether the current GOOS provides a complete
// initial-admin bootstrap implementation. On linux, darwin, and windows the
// credfile security primitives (POSIX 0600 / Windows DACL), file IO, sweep,
// and scheduler are all available, so this build returns nil.
//
// Callers (typically Cell.Init when WithInitialAdminBootstrap is active)
// invoke this before binding lifecycle dependencies so platform mismatches
// fail fast at phase2 rather than during phase3b OnStart.
func PlatformSupported() error { return nil }
