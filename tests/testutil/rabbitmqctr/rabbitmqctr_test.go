//go:build integration

package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartRabbitMQContainer_StartsReadyAMQP(t *testing.T) {
	ctx := context.Background()
	container := StartRabbitMQContainer(t, ctx)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(ctx), "terminate rabbitmq container")
	})

	amqpURL, err := container.AmqpURL(ctx)
	require.NoError(t, err, "get rabbitmq amqp url")
	require.NotEmpty(t, amqpURL)
}
