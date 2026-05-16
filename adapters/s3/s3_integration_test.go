//go:build integration

package s3

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/testutil"
)

const (
	integrationBucket = "gocell-s3-test"
	integrationRegion = "us-east-1"
)

// buildEndpoint constructs a http:// endpoint from a raw host:port connection
// string returned by tcminio.MinioContainer.ConnectionString, and applies the
// loopback rewrite needed on macOS where testcontainers returns "localhost".
func buildEndpoint(connStr string) string {
	return testutil.LoopbackIPEndpoint("http://" + connStr)
}

// createBucket creates the integration bucket via a raw AWS SDK client
// (path-style, no TLS). Must be called before s3.New because New runs a sync
// HeadBucket probe that fails if the bucket does not exist.
func createBucket(t *testing.T, ctx context.Context, endpoint, user, pass string) {
	t.Helper()
	awsCfg := aws.Config{
		Region:      integrationRegion,
		Credentials: credentials.NewStaticCredentialsProvider(user, pass, ""),
	}
	rawClient := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	_, err := rawClient.CreateBucket(ctx, &awss3.CreateBucketInput{
		Bucket: aws.String(integrationBucket),
	})
	require.NoError(t, err, "create integration bucket")
}

// newIntegrationConfig returns a Config tuned for integration tests:
//   - HTTPTimeout 500ms so AWS SDK retries (default 3×) fail fast (~1.5s total)
//     and do not exhaust the Eventually budget.
//   - HealthInterval 100ms so the background worker flips state quickly.
func newIntegrationConfig(endpoint, user, pass string) Config {
	return Config{
		Endpoint:        endpoint,
		Region:          integrationRegion,
		Bucket:          integrationBucket,
		AccessKeyID:     user,
		SecretAccessKey: pass,
		UsePathStyle:    true,
		HTTPTimeout:     testtime.D500ms, // fast-fail: 3 retries × 500ms ≈ 1.5s
		HealthInterval:  testtime.D100ms,
		Clock:           clock.Real(),
	}
}

// TestIntegration_S3_UploadHealthHappy verifies the happy-path:
//   - s3.New succeeds (sync HeadBucket probe passes)
//   - Upload stores an object without error
//   - Health() returns nil (live HeadBucket call)
//   - Checkers()["s3_ready"] returns nil (atomic state read, no network)
func TestIntegration_S3_UploadHealthHappy(t *testing.T) {
	ctx := context.Background()

	// No persistent volume needed — this test does not survive stop/start.
	ctr := testutil.StartMinIOContainer(t, ctx)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	connStr, err := ctr.ConnectionString(ctx)
	require.NoError(t, err, "get minio connection string")

	endpoint := buildEndpoint(connStr)
	createBucket(t, ctx, endpoint, ctr.Username, ctr.Password)

	cfg := newIntegrationConfig(endpoint, ctr.Username, ctr.Password)
	client, err := New(ctx, cfg)
	require.NoError(t, err, "s3.New should succeed with real MinIO")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer cancel()
		_ = client.Close(closeCtx)
	})

	// Upload a small object.
	assert.NoError(t, client.Upload(ctx, "test-key", []byte("hello"), "text/plain"),
		"Upload should succeed")

	// Direct health check (live HeadBucket network call).
	assert.NoError(t, client.Health(ctx), "Health should be nil after successful upload")

	// State-machine probe: reads atomic state, no network I/O.
	checkers := client.Checkers()
	require.Contains(t, checkers, "s3_ready")
	assert.NoError(t, checkers["s3_ready"](ctx), "s3_ready checker should be nil")
}

// TestIntegration_S3_RecoveryAfterContainerRestart verifies the stop/start
// recovery path:
//
//  1. Health() is OK while container is running.
//  2. After container.Stop(), Health() returns a non-nil error.
//  3. After container.Start(), a new client constructed against the fresh endpoint
//     (Docker may reassign the host port on restart) succeeds: Health() returns nil.
//
// A named volume keeps the bucket data alive across stop/start so HeadBucket
// finds the bucket on recovery — without it MinIO would lose in-memory state and
// return NoSuchBucket (permanent), masking the transient→recovery path.
func TestIntegration_S3_RecoveryAfterContainerRestart(t *testing.T) {
	ctx := context.Background()

	volumeName := fmt.Sprintf("s3-integ-restart-%d", time.Now().UnixNano())

	ctr := testutil.StartMinIOContainer(t, ctx,
		testcontainers.WithMounts(
			testcontainers.VolumeMount(volumeName, "/data"),
		),
	)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	connStr, err := ctr.ConnectionString(ctx)
	require.NoError(t, err)
	endpoint := buildEndpoint(connStr)

	createBucket(t, ctx, endpoint, ctr.Username, ctr.Password)

	cfg := newIntegrationConfig(endpoint, ctr.Username, ctr.Password)
	client, err := New(ctx, cfg)
	require.NoError(t, err, "s3.New initial construction")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer cancel()
		_ = client.Close(closeCtx)
	})

	// Baseline: Health should be OK before stop.
	require.NoError(t, client.Health(ctx), "Health should be OK before stop")

	// Stop the container. Health() should now fail.
	// "connection refused" is classified permanent by classifyS3Error fail-closed;
	// we only assert non-nil, not IsTransient.
	stopTimeout := 5 * time.Second
	require.NoError(t, ctr.Stop(ctx, &stopTimeout), "stop container")

	healthErr := client.Health(ctx)
	require.Error(t, healthErr, "Health should return an error after container stop")

	// Restart the container. Docker may assign a new host port, so we re-read
	// ConnectionString after Start to get the current mapping.
	require.NoError(t, ctr.Start(ctx), "restart container")

	newConnStr, err := ctr.ConnectionString(ctx)
	require.NoError(t, err, "get connection string after restart")
	newEndpoint := buildEndpoint(newConnStr)

	newCfg := newIntegrationConfig(newEndpoint, ctr.Username, ctr.Password)
	newClient, err := New(ctx, newCfg)
	require.NoError(t, err, "s3.New on fresh endpoint after restart")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer cancel()
		_ = newClient.Close(closeCtx)
	})

	assert.NoError(t, newClient.Health(ctx),
		"Health on new client should succeed after container restart")
}

// TestIntegration_S3_WorkerTickStateTracksContainer verifies that the background
// health-probe worker updates the atomic state as the container goes down and
// comes back up.
//
// After container restart Docker may reassign the host port, so recovery is
// verified by constructing a new client (with the fresh endpoint) and confirming
// its Checkers()["s3_ready"] flips to nil after the first worker tick.
func TestIntegration_S3_WorkerTickStateTracksContainer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	volumeName := fmt.Sprintf("s3-integ-worker-%d", time.Now().UnixNano())

	ctr := testutil.StartMinIOContainer(t, ctx,
		testcontainers.WithMounts(
			testcontainers.VolumeMount(volumeName, "/data"),
		),
	)
	t.Cleanup(func() {
		termCtx, termCancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer termCancel()
		_ = ctr.Terminate(termCtx)
	})

	connStr, err := ctr.ConnectionString(ctx)
	require.NoError(t, err)
	endpoint := buildEndpoint(connStr)

	createBucket(t, ctx, endpoint, ctr.Username, ctr.Password)

	cfg := newIntegrationConfig(endpoint, ctr.Username, ctr.Password)
	client, err := New(ctx, cfg)
	require.NoError(t, err, "s3.New for worker test")

	// Start the background health worker.
	w := client.Worker()
	workerCtx, workerCancel := context.WithCancel(ctx)
	t.Cleanup(func() {
		workerCancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer stopCancel()
		_ = w.Stop(stopCtx)
	})
	go func() { _ = w.Start(workerCtx) }()

	// Wait for the worker to observe a healthy state on its first tick.
	checkers := client.Checkers()
	require.Contains(t, checkers, "s3_ready")
	require.Eventually(t, func() bool {
		return checkers["s3_ready"](ctx) == nil
	}, testtime.EventuallyLong, testtime.SlowPoll,
		"s3_ready should be nil once worker probes healthy MinIO")

	// Stop the container. Worker tick will call HeadBucket and update state to
	// non-nil. We assert the probe becomes non-nil.
	stopTimeout := 5 * time.Second
	require.NoError(t, ctr.Stop(ctx, &stopTimeout), "stop container for worker test")

	require.Eventually(t, func() bool {
		return checkers["s3_ready"](ctx) != nil
	}, testtime.EventuallyExtraLong, testtime.SlowPoll,
		"s3_ready should flip to non-nil error while container is stopped")

	// Restart the container. Docker may reassign the host port, so we construct
	// a new client against the fresh endpoint and start its worker to verify
	// the recovery path.
	require.NoError(t, ctr.Start(ctx), "restart container for worker test")

	newConnStr, err := ctr.ConnectionString(ctx)
	require.NoError(t, err, "get connection string after restart")
	newEndpoint := buildEndpoint(newConnStr)

	newCfg := newIntegrationConfig(newEndpoint, ctr.Username, ctr.Password)
	newClient, err := New(ctx, newCfg)
	require.NoError(t, err, "s3.New on fresh endpoint after restart")

	newW := newClient.Worker()
	go func() { _ = newW.Start(ctx) }()
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer stopCancel()
		_ = newW.Stop(stopCtx)
		closeCtx, closeCancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer closeCancel()
		_ = newClient.Close(closeCtx)
	})

	newCheckers := newClient.Checkers()
	require.Contains(t, newCheckers, "s3_ready")
	require.Eventually(t, func() bool {
		return newCheckers["s3_ready"](ctx) == nil
	}, testtime.EventuallyExtraLong, testtime.SlowPoll,
		"s3_ready on new client should recover to nil after container restart")
}
