//go:build linux

package initialadmin

// defaultStateDir returns the default state directory for Linux systems.
// Follows the systemd RuntimeDirectory convention: /run/gocell is created by
// the service manager with appropriate ownership before the process starts.
func defaultStateDir() (string, error) {
	return "/run/gocell", nil
}
