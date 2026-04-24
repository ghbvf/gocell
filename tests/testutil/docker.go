// Package testutil provides shared test utilities for integration tests.
package testutil

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// RequireDocker skips t if Docker is not available in the test environment.
// Integration tests that use testcontainers must call this at the top of the
// test (or setup helper) so they self-skip in CI environments without Docker.
//
// Detection strategy:
//  1. DOCKER_HOST env var — if set and non-empty, assume Docker is available.
//  2. Default Unix socket /var/run/docker.sock on Unix targets.
//  3. `docker info` fallback through fixed Docker Desktop / distro CLI paths.
//
// This avoids importing the Docker client SDK while remaining correct for the
// common CI cases (socket present or DOCKER_HOST set).
func RequireDocker(t *testing.T) {
	t.Helper()
	if dockerAvailable() {
		return
	}
	t.Skip("docker not available; skipping integration test")
}

// dockerAvailable returns true when Docker appears reachable.
func dockerAvailable() bool {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return true
	}
	if runtime.GOOS != "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
		if err == nil {
			_ = conn.Close()
			return true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return dockerCLIAvailable(ctx)
}

func dockerCLIAvailable(ctx context.Context) bool {
	for _, dockerPath := range dockerCLIPaths() {
		info, err := os.Stat(dockerPath)
		if err != nil || info.IsDir() {
			continue
		}
		cmd := exec.CommandContext(ctx, dockerPath, "info", "--format", "{{.ServerVersion}}")
		if cmd.Run() == nil {
			return true
		}
	}
	return false
}

func dockerCLIPaths() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
			`C:\Program Files\Docker\Docker\resources\bin\com.docker.cli.exe`,
		}
	case "darwin":
		return []string{
			"/usr/local/bin/docker",
			"/opt/homebrew/bin/docker",
			"/Applications/Docker.app/Contents/Resources/bin/docker",
		}
	default:
		return []string{
			"/usr/bin/docker",
			"/usr/local/bin/docker",
			"/snap/bin/docker",
		}
	}
}
