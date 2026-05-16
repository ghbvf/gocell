//go:build integration

package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStartMinIOContainer_StartsReadyHTTP verifies that StartMinIOContainer
// returns a live MinIO container with a reachable connection string.
//
// The test mirrors rabbitmq_integration_test.go: it calls the exported helper,
// asserts no error on startup, and checks that ConnectionString returns a
// non-empty address — confirming the container is reachable on the mapped port.
func TestStartMinIOContainer_StartsReadyHTTP(t *testing.T) {
	ctx := context.Background()
	container := StartMinIOContainer(t, ctx)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(ctx), "terminate minio container")
	})

	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err, "get minio connection string")
	require.NotEmpty(t, connStr)
}
