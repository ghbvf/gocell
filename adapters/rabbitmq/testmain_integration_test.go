//go:build integration

package rabbitmq

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil"
)

// Shared broker lifecycle for the rabbitmq integration package.
//
// All tests that do NOT mutate broker-wide state (rabbitmqctl, container
// restart) reuse one testcontainers RabbitMQ via sharedBrokerURL. The
// container is started lazily on first use and terminated from TestMain
// after the whole package finishes, NOT via t.Cleanup.
//
// Previously, each test function that called startRabbitMQ paid a fresh
// container startup (~5-7s each × 5 callers = 25-35s wall time).
//
// Tests with broker-wide side effects (TestIntegration_ConnectionRecovery
// calls rabbitmqctl close_all_connections) MUST continue using
// startRabbitMQDedicatedContainer to get their own container.
var (
	sharedBrokerOnce     sync.Once
	sharedBrokerInitErr  error
	sharedBrokerURLValue string
	sharedBrokerShutdown func()
)

// sharedBrokerURL starts (once) and returns the amqp URL of the package-
// wide shared broker. Tests use it with newIntegrationConnection to get a
// per-test Connection with its own cleanup.
func sharedBrokerURL(t *testing.T) string {
	t.Helper()

	sharedBrokerOnce.Do(func() {
		ctx := context.Background()
		container, err := testutil.StartRabbitMQContainerE(t, ctx)
		if err != nil {
			sharedBrokerInitErr = fmt.Errorf("start shared rabbitmq container: %w", err)
			return
		}
		u, err := container.AmqpURL(ctx)
		if err != nil {
			if termErr := container.Terminate(ctx); termErr != nil {
				slog.Default().Warn("rabbitmq: shared broker terminate after AmqpURL failure", "error", termErr)
			}
			sharedBrokerInitErr = fmt.Errorf("get shared rabbitmq url: %w", err)
			return
		}
		sharedBrokerURLValue = u
		sharedBrokerShutdown = func() {
			if err := container.Terminate(ctx); err != nil {
				// TestMain runs outside any test context, so *testing.T is
				// unavailable. Emit to stderr so CI log scrapers pick up
				// cleanup failures (Ryuk fallback handles the actual reap,
				// but a failure here usually hints at Docker-daemon trouble).
				slog.Default().Warn("rabbitmq: shared broker terminate failed", "error", err)
			}
		}
	})
	if sharedBrokerInitErr != nil {
		t.Fatalf("shared broker unavailable: %v", sharedBrokerInitErr)
	}
	return sharedBrokerURLValue
}

// TestMain owns shared-broker teardown. It runs exactly once per test
// binary invocation, after all tests in the package have completed.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedBrokerShutdown != nil {
		sharedBrokerShutdown()
	}
	os.Exit(code)
}
