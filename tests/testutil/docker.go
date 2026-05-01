// Package testutil provides shared test utilities for integration tests.
package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

const dockerProviderHealthTimeout = 10 * time.Second

type dockerHealthProvider interface {
	Health(context.Context) error
}

var (
	getDockerProvider = func() (dockerHealthProvider, error) {
		return testcontainers.ProviderDocker.GetProvider()
	}
	skipIfDockerProviderUnhealthy = testcontainers.SkipIfProviderIsNotHealthy
)

// RequireDocker skips t if Docker is not available in a local test environment.
// Integration tests that use testcontainers must call this at the top of the
// test (or setup helper) so local runs self-skip when Docker is unavailable.
//
// Set GOCELL_TEST_DOCKER_REQUIRED=1 in CI jobs where Docker-backed integration
// tests are mandatory. In that mode an unavailable provider is a test failure,
// not a skip, so CI cannot go green without executing the integration path.
func RequireDocker(t *testing.T) {
	t.Helper()
	if dockerRequired() {
		ctx, cancel := context.WithTimeout(context.Background(), dockerProviderHealthTimeout)
		defer cancel()
		if err := dockerProviderHealth(ctx); err != nil {
			t.Fatal(requireDockerFailureMessage(err))
		}
		return
	}

	skipIfDockerProviderUnhealthy(t)
}

func dockerRequired() bool {
	return os.Getenv("GOCELL_TEST_DOCKER_REQUIRED") == "1"
}

func dockerProviderHealth(ctx context.Context) error {
	provider, err := getDockerProvider()
	if err != nil {
		return err
	}
	return provider.Health(ctx)
}

func requireDockerFailureMessage(err error) string {
	return fmt.Sprintf(
		"docker provider required by GOCELL_TEST_DOCKER_REQUIRED=1 but unhealthy or unavailable"+
			" (DOCKER_HOST=%q): %v; start Docker or unset GOCELL_TEST_DOCKER_REQUIRED for local self-skip",
		os.Getenv("DOCKER_HOST"),
		err,
	)
}
