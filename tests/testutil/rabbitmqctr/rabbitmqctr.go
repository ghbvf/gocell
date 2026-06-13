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

const (
	rabbitMQStartupTimeout = 30 * time.Second
	rabbitMQCleanupTimeout = 10 * time.Second
)

// StartRabbitMQContainer starts a RabbitMQ testcontainer with the shared
// readiness contract used by all integration tests. Unlike
// minioctr.StartMinIOContainer, this helper does NOT register t.Cleanup —
// the caller owns the returned container's lifetime and MUST Terminate it in
// its own cleanup path. On a start error nothing is returned to own (see
// StartRabbitMQContainerE), so the success path is the only one the caller
// must clean up after.
func StartRabbitMQContainer(t *testing.T, ctx context.Context) *tcrabbitmq.RabbitMQContainer {
	t.Helper()
	container, err := StartRabbitMQContainerE(t, ctx)
	require.NoError(t, err, "start rabbitmq container")
	return container
}

// StartRabbitMQContainerE is the error-returning variant of StartRabbitMQContainer.
// Unlike minioctr.StartMinIOContainer, this helper does NOT register t.Cleanup —
// on success the caller owns the returned container's lifetime and MUST Terminate
// it in its own cleanup path. A partial start (tcrabbitmq.Run returns a non-nil
// container alongside a non-nil error, e.g. a readiness-wait timeout after a
// successful docker create) is handled here: the orphan is Terminated before the
// error is surfaced, so the error path returns a nil container with nothing to
// clean up.
func StartRabbitMQContainerE(t *testing.T, ctx context.Context) (*tcrabbitmq.RabbitMQContainer, error) {
	t.Helper()
	testutil.RequireDocker(t)

	container, err := tcrabbitmq.Run(
		ctx, testutil.RabbitMQImage,
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForListeningPort(tcrabbitmq.DefaultAMQPPort).
				WithStartupTimeout(rabbitMQStartupTimeout),
		),
	)
	if err != nil {
		// Partial start: terminate the orphan with a bounded context so the
		// caller-owned lifecycle contract only applies to the success path.
		if container != nil {
			termCtx, cancel := context.WithTimeout(context.Background(), rabbitMQCleanupTimeout)
			defer cancel()
			_ = container.Terminate(termCtx)
		}
		return nil, err
	}
	return container, nil
}
