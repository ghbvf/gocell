//go:build darwin

package initialadmin

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultStateDir returns the default state directory for macOS.
// Uses the user's Library/Application Support directory as per macOS conventions:
// $HOME/Library/Application Support/gocell/run.
func defaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("initialadmin: resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "gocell", "run"), nil
}
