//go:build unix || windows

package initialadmin

// PlatformSupported returns nil on supported platforms (unix and windows).
// cell.Init calls this before binding repos so that an unsupported GOOS
// surfaces immediately during Init, not later when OnStart runs.
func PlatformSupported() error { return nil }
