//go:build !linux && !darwin && !windows

package initialadmin

import "errors"

// defaultStateDir returns an error on unsupported platforms.
// Set GOCELL_STATE_DIR to provide an explicit state directory.
func defaultStateDir() (string, error) {
	return "", errors.New("initialadmin: no default state directory on this platform; set GOCELL_STATE_DIR")
}
