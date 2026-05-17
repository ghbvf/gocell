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

const (
	minioStartupTimeout = 30 * time.Second
	minioCleanupTimeout = 10 * time.Second
)

// MinIORunOption configures the MinIO testcontainer.
type MinIORunOption func(*minioRunConfig)

type minioRunConfig struct {
	containerOpts  []testcontainers.ContainerCustomizer
	cleanupVolumes []string
}

func newMinioRunConfig(opts []MinIORunOption) *minioRunConfig {
	cfg := &minioRunConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// WithMinIOVolume mounts a named volume at the container target path and, when
// used with StartMinIOContainer, registers RemoveVolumes on cleanup. Callers
// of StartMinIOContainer MUST NOT register their own Terminate cleanup; the
// helper handles Terminate + RemoveVolumes. Callers that invoke tcminio.Run
// directly own container lifetime themselves and must handle RemoveVolumes
// in their own cleanup path; the cleanupVolumes hint is informational only.
func WithMinIOVolume(name, target string) MinIORunOption {
	return func(c *minioRunConfig) {
		c.containerOpts = append(c.containerOpts,
			testcontainers.WithMounts(
				testcontainers.VolumeMount(name, testcontainers.ContainerMountTarget(target)),
			),
		)
		c.cleanupVolumes = append(c.cleanupVolumes, name)
	}
}

// MinIORunOptions returns the resolved []testcontainers.ContainerCustomizer
// (shared image readiness wait strategy plus any caller-supplied opts).
// It does NOT call tcminio.Run — caller invokes tcminio.Run themselves with
// these options, so the caller's own function carries the
// testutil.RequireDocker(t) guard (INTEGRATION-DOCKER-GUARD archtest).
//
// Used by TestMain-scoped package-shared containers (e.g. sharedMinIOContainer)
// where container lifetime is owned outside any single test t. Per-test
// containers should use StartMinIOContainer which handles RequireDocker +
// t.Cleanup automatically.
func MinIORunOptions(opts ...MinIORunOption) []testcontainers.ContainerCustomizer {
	cfg := newMinioRunConfig(opts)
	return resolveContainerOpts(cfg)
}

func resolveContainerOpts(cfg *minioRunConfig) []testcontainers.ContainerCustomizer {
	return append([]testcontainers.ContainerCustomizer{
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForHTTP("/minio/health/live").
				WithPort("9000").
				WithStartupTimeout(minioStartupTimeout),
		),
	}, cfg.containerOpts...)
}

// StartMinIOContainer starts a MinIO testcontainer and registers all cleanup
// (Terminate + RemoveVolumes) on t.Cleanup. Partial start (container != nil
// alongside a non-nil error from tcminio.Run) is handled too — cleanup is
// registered before the error is surfaced, so no container or volume leaks.
//
// Callers MUST NOT register their own Terminate cleanup.
//
// ref: testcontainers-go/modules/minio Run — defaults: user/pass = minioadmin,
// "/minio/health/live" HTTP readiness on port 9000.
func StartMinIOContainer(t *testing.T, ctx context.Context, opts ...MinIORunOption) *tcminio.MinioContainer {
	t.Helper()
	RequireDocker(t)

	cfg := newMinioRunConfig(opts)
	container, err := tcminio.Run(ctx, MinIOImage, resolveContainerOpts(cfg)...)

	// Register cleanup before checking err: tcminio.Run can return a non-nil
	// container alongside a non-nil error (Inspect failure after a successful
	// docker create). Skipping cleanup on the error branch would leak the
	// container and any named volumes.
	if container != nil {
		registerMinIOCleanup(t, container, cfg.cleanupVolumes)
	}

	require.NoError(t, err, "start minio container")
	return container
}

func registerMinIOCleanup(t *testing.T, container *tcminio.MinioContainer, volumes []string) {
	t.Helper()
	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), minioCleanupTimeout)
		defer cancel()
		terminateOpts := make([]testcontainers.TerminateOption, 0, len(volumes))
		for _, v := range volumes {
			terminateOpts = append(terminateOpts, testcontainers.RemoveVolumes(v))
		}
		_ = container.Terminate(termCtx, terminateOpts...)
	})
}
