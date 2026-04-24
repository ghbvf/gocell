package initialadmin

import (
	"fmt"
	"os"
	"path/filepath"
)

// stateFileName is the credential file basename appended to the resolved
// state directory.
const stateFileName = "initial_admin_password"

// ResolveCredentialPath returns the absolute path of the initial-admin
// credential file. Resolution precedence:
//
//  1. Explicit stateDir argument (if non-empty).
//  2. GOCELL_STATE_DIR environment variable (if set).
//  3. Platform-specific defaultStateDir() (build-tag implementations in
//     path_default_{linux,darwin,windows,unsupported}.go).
//
// The result is filepath.Clean'd. stateDir / GOCELL_STATE_DIR must be
// absolute; a relative value causes a fail-fast error rather than a
// silent path-traversal hazard.
func ResolveCredentialPath(stateDir string) (string, error) {
	dir := stateDir
	if dir == "" {
		dir = os.Getenv("GOCELL_STATE_DIR")
	}
	if dir == "" {
		var err error
		dir, err = defaultStateDir()
		if err != nil {
			return "", fmt.Errorf("initialadmin: resolve default state dir: %w", err)
		}
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("initialadmin: state directory must be absolute, got %q", dir)
	}
	return filepath.Clean(filepath.Join(dir, stateFileName)), nil
}
