//go:build darwin

package initialadmin

import (
	"fmt"
	"os"
	"path/filepath"
)

func defaultCredentialDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("initialadmin: resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "gocell", "run"), nil
}
