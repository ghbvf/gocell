//go:build integration

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"
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
	RequireDocker(t)

	return tcrabbitmq.Run(ctx, RabbitMQImage,
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForListeningPort(nat.Port(tcrabbitmq.DefaultAMQPPort)).
				WithStartupTimeout(rabbitMQStartupTimeout),
		),
	)
}
