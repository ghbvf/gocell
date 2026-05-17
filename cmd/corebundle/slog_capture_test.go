package main

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/health/healthtest"
)

// withSlogCapture redirects slog.Default for the duration of the test.
// Delegates to healthtest.NewCapture.
func withSlogCapture(t *testing.T) *healthtest.CaptureHandler {
	t.Helper()
	return healthtest.NewCapture(t)
}

// readyzUnhealthyDeps fetches the verbose-breakdown dependencies map from
// captured slog records. Delegates to healthtest.ReadyzUnhealthyDeps.
func readyzUnhealthyDeps(t *testing.T, capture *healthtest.CaptureHandler) map[string]health.SlogDependencyEntry {
	t.Helper()
	return healthtest.ReadyzUnhealthyDeps(t, capture)
}
