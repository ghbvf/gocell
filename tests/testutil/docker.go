// Package testutil provides shared test utilities for integration tests.
package testutil

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// RequireDocker skips t if Docker is not available in the test environment.
// Integration tests that use testcontainers must call this at the top of the
// test (or setup helper) so they self-skip in CI environments without Docker.
//
// Detection strategy:
//  1. DOCKER_HOST env var — if set and non-empty, assume Docker is available.
//  2. Default Unix socket /var/run/docker.sock — try a 1-second TCP dial.
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
	// Try the default Unix socket via a short-lived TCP dial.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
