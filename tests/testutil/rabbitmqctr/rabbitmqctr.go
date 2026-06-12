//go:build integration

package rabbitmqctr

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ghbvf/gocell/tests/testutil"
)

const rabbitMQStartupTimeout = 30 * time.Second

// StartRabbitMQContainer starts a RabbitMQ testcontainer with the shared
// readiness contract used by all integration tests.
func StartRabbitMQContainer(t *testing.T, ctx context.Context) *tcrabbitmq.RabbitMQContainer {
	t.Helper()
	container, err := StartRabbitMQContainerE(t, ctx)
	require.NoError(t, err, "start rabbitmq container")
	return container
}

func StartRabbitMQContainerE(t *testing.T, ctx context.Context) (*tcrabbitmq.RabbitMQContainer, error) {
	t.Helper()
	testutil.RequireDocker(t)

	return tcrabbitmq.Run(ctx, testutil.RabbitMQImage,
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForListeningPort(tcrabbitmq.DefaultAMQPPort).
				WithStartupTimeout(rabbitMQStartupTimeout),
		),
	)
}
