package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/require"
)

const testServiceSecret = "test-service-secret-at-least-32-bytes!!"

func TestInternalAuthChainMissingServiceSecretFailsFast(t *testing.T) {
	t.Setenv(iotdeviceServiceSecretEnv, "")

	_, err := newInternalAuthChainFromEnv()

	require.Error(t, err)
	require.Contains(t, err.Error(), iotdeviceServiceSecretEnv)
}

func TestInternalAuthChainContainsServiceToken(t *testing.T) {
	t.Setenv(iotdeviceServiceSecretEnv, testServiceSecret)

	chain, err := newInternalAuthChainFromEnv()

	require.NoError(t, err)
	require.NotEmpty(t, chain)
	require.True(t, authChainContainsServiceToken(chain))
}

func TestSourceDoesNotContainDemoTokenVerifier(t *testing.T) {
	forbidden := []string{
		"demo" + "Token" + "Verifier",
		"demo" + "Admin" + "Token",
		"iotdevice-admin-" + "demo-token",
	}

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range forbidden {
			if strings.Contains(string(src), needle) {
				return fmt.Errorf("%s still contains %q", path, needle)
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func authChainContainsServiceToken(chain []cell.ListenerAuth) bool {
	for _, plan := range chain {
		if _, ok := plan.(cell.AuthServiceToken); ok {
			return true
		}
	}
	return false
}
