//go:build !linux && !darwin && !windows

package initialadmin

import "github.com/ghbvf/gocell/pkg/errcode"

// defaultStateDir returns an error on unsupported platforms. Set
// GOCELL_STATE_DIR to provide an explicit state directory. The error carries
// ErrCellPlatformUnsupported so the failure surfaces with the same code as
// PlatformSupported() / newBootstrapper() on non-unix non-windows builds.
func defaultStateDir() (string, error) {
	return "", errcode.New(errcode.KindInternal, errcode.ErrCellPlatformUnsupported,
		"initialadmin: no default state directory on this platform; set GOCELL_STATE_DIR")
}
