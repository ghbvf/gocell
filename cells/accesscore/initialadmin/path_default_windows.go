//go:build windows

package initialadmin

import (
	"fmt"
	"os"
	"path/filepath"
)

func defaultCredentialDir() (string, error) {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "gocell", "run"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("initialadmin: resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "gocell", "run"), nil
}
