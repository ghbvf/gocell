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
// The helper already registers Terminate on t.Cleanup; the test asserts only
// connection reachability.
func TestStartMinIOContainer_StartsReadyHTTP(t *testing.T) {
	ctx := context.Background()
	container := StartMinIOContainer(t, ctx)

	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err, "get minio connection string")
	require.NotEmpty(t, connStr)
}
