//go:build windows

package initialadmin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// defaultStateDir returns the default state directory for Windows.
// Prefers %LOCALAPPDATA%\gocell\run; falls back to os.UserConfigDir() if
// LOCALAPPDATA is not set.
func defaultStateDir() (string, error) {
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return filepath.Join(localAppData, "gocell", "run"), nil
	}
	// Fall back to os.UserConfigDir which returns the AppData\Roaming path on Windows.
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("initialadmin: resolve user config dir: %w", err)
	}
	if configDir == "" {
		return "", errors.New("initialadmin: could not determine Windows config directory; set GOCELL_STATE_DIR")
	}
	return filepath.Join(configDir, "gocell", "run"), nil
}
