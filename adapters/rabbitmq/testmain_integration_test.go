//go:build integration

package rabbitmq

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"

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
		// tcrabbitmq.Run's default wait strategy only watches for the
		// "Server startup complete" log pattern. On Docker Desktop for Mac
		// the port forwarder can lag behind that signal, causing AmqpURL /
		// PortEndpoint to return `port "5672/tcp" not found` for a brief
		// window. Add an explicit port-listening wait so the container is
		// not considered ready until the mapped port is actually reachable.
		container, err := tcrabbitmq.Run(ctx, testutil.RabbitMQImage,
			testcontainers.WithAdditionalWaitStrategy(
				wait.ForListeningPort(nat.Port(tcrabbitmq.DefaultAMQPPort)).
					WithStartupTimeout(30*time.Second),
			),
		)
		if err != nil {
			sharedBrokerInitErr = fmt.Errorf("start shared rabbitmq container: %w", err)
			return
		}
		u, err := container.AmqpURL(ctx)
		if err != nil {
			if termErr := container.Terminate(ctx); termErr != nil {
				log.Printf("rabbitmq: shared broker terminate after AmqpURL failure: %v", termErr)
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
				log.Printf("rabbitmq: shared broker terminate failed: %v", err)
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
