package testutil

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

type fakeDockerProvider struct {
	healthErr error
}

func (p fakeDockerProvider) Health(context.Context) error {
	return p.healthErr
}

func TestDockerRequiredEnv(t *testing.T) {
	t.Setenv("GOCELL_TEST_DOCKER_REQUIRED", "")
	t.Setenv("CI", "")
	if dockerRequired() {
		t.Fatal("dockerRequired() = true with no env, want false")
	}

	t.Setenv("GOCELL_TEST_DOCKER_REQUIRED", "1")
	if !dockerRequired() {
		t.Fatal("dockerRequired() = false with GOCELL_TEST_DOCKER_REQUIRED=1")
	}

	t.Setenv("GOCELL_TEST_DOCKER_REQUIRED", "")
	t.Setenv("CI", "true")
	if dockerRequired() {
		t.Fatal("dockerRequired() = true with CI=true, want explicit GOCELL_TEST_DOCKER_REQUIRED only")
	}
}

func TestDockerProviderHealth(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		restoreDockerProvider(t, func() (dockerHealthProvider, error) {
			return fakeDockerProvider{}, nil
		})

		if err := dockerProviderHealth(context.Background()); err != nil {
			t.Fatalf("dockerProviderHealth() error = %v, want nil", err)
		}
	})

	t.Run("provider error", func(t *testing.T) {
		providerErr := errors.New("provider unavailable")
		restoreDockerProvider(t, func() (dockerHealthProvider, error) {
			return nil, providerErr
		})

		if err := dockerProviderHealth(context.Background()); !errors.Is(err, providerErr) {
			t.Fatalf("dockerProviderHealth() error = %v, want %v", err, providerErr)
		}
	})

	t.Run("health error", func(t *testing.T) {
		healthErr := errors.New("provider unhealthy")
		restoreDockerProvider(t, func() (dockerHealthProvider, error) {
			return fakeDockerProvider{healthErr: healthErr}, nil
		})

		if err := dockerProviderHealth(context.Background()); !errors.Is(err, healthErr) {
			t.Fatalf("dockerProviderHealth() error = %v, want %v", err, healthErr)
		}
	})
}

func TestDockerProviderHealth_DefaultProviderWhenAvailable(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	if err := dockerProviderHealth(context.Background()); err != nil {
		t.Fatalf("dockerProviderHealth() with default provider error = %v, want nil", err)
	}
}

func TestRequireDocker(t *testing.T) {
	t.Run("strict checks provider health", func(t *testing.T) {
		t.Setenv("GOCELL_TEST_DOCKER_REQUIRED", "1")
		restoreDockerProvider(t, func() (dockerHealthProvider, error) {
			return fakeDockerProvider{}, nil
		})

		RequireDocker(t)
	})

	t.Run("local delegates to testcontainers skip helper", func(t *testing.T) {
		t.Setenv("GOCELL_TEST_DOCKER_REQUIRED", "")
		t.Setenv("CI", "")
		var called bool
		restoreDockerSkip(t, func(*testing.T) {
			called = true
		})

		RequireDocker(t)
		if !called {
			t.Fatal("RequireDocker() did not delegate to skip helper")
		}
	})
}

func TestRequireDockerFailureMessage(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/docker.sock")

	message := requireDockerFailureMessage(errors.New("daemon down"))
	for _, want := range []string{
		"GOCELL_TEST_DOCKER_REQUIRED=1",
		`DOCKER_HOST="unix:///tmp/docker.sock"`,
		"daemon down",
		"start Docker",
		"local self-skip",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("requireDockerFailureMessage() = %q, want substring %q", message, want)
		}
	}
}

func restoreDockerProvider(t *testing.T, provider func() (dockerHealthProvider, error)) {
	t.Helper()
	previous := getDockerProvider
	getDockerProvider = provider
	t.Cleanup(func() {
		getDockerProvider = previous
	})
}

func restoreDockerSkip(t *testing.T, skip func(*testing.T)) {
	t.Helper()
	previous := skipIfDockerProviderUnhealthy
	skipIfDockerProviderUnhealthy = skip
	t.Cleanup(func() {
		skipIfDockerProviderUnhealthy = previous
	})
}
