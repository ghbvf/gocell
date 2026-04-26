//go:build !linux && !darwin && !windows

package initialadmin

import "github.com/ghbvf/gocell/pkg/errcode"

// PlatformSupported reports whether the current GOOS provides a complete
// initial-admin bootstrap implementation. On platforms other than linux,
// darwin, and windows the credfile security primitives are unavailable (see
// credfile_unsupported.go / sweep_unsupported.go), so this returns
// ErrCellPlatformUnsupported. Callers invoke this from cell.Init() when
// WithInitialAdminBootstrap is active so the failure surfaces at process
// startup rather than during phase3b OnStart.
func PlatformSupported() error {
	return errcode.New(errcode.ErrCellPlatformUnsupported,
		"initialadmin: bootstrap requires linux, darwin, or windows; remove WithInitialAdminBootstrap() or build for a supported GOOS")
}
