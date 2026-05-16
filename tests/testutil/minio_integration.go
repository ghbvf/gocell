//go:build integration

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	"github.com/testcontainers/testcontainers-go/wait"
)

const minioStartupTimeout = 30 * time.Second

// StartMinIOContainer starts a MinIO testcontainer with the shared readiness
// contract used by all integration tests. Fails the test on container start
// errors. Use StartMinIOContainerE if the caller wants to control error handling
// (e.g. to attempt teardown on partial start).
//
// ref: testcontainers-go/modules/minio Run — defaults: user/pass = minioadmin,
// "/minio/health/live" HTTP readiness on port 9000.
func StartMinIOContainer(t *testing.T, ctx context.Context, opts ...testcontainers.ContainerCustomizer) *tcminio.MinioContainer {
	t.Helper()
	container, err := StartMinIOContainerE(t, ctx, opts...)
	require.NoError(t, err, "start minio container")
	return container
}

// StartMinIOContainerE returns the container and error separately so callers
// can attempt graceful teardown on partial start failures.
func StartMinIOContainerE(t *testing.T, ctx context.Context, opts ...testcontainers.ContainerCustomizer) (*tcminio.MinioContainer, error) {
	t.Helper()
	RequireDocker(t)

	allOpts := append([]testcontainers.ContainerCustomizer{
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForHTTP("/minio/health/live").
				WithPort("9000").
				WithStartupTimeout(minioStartupTimeout),
		),
	}, opts...)

	return tcminio.Run(ctx, MinIOImage, allOpts...)
}
