package s3

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/secutil"
)

const (
	// defaultS3HTTPTimeout is the default HTTP client timeout for S3 operations.
	defaultS3HTTPTimeout = 30 * time.Second

	// defaultS3HealthInterval is the default background health probe interval.
	// NO magic literal: this const is the single source of truth for the 30s default.
	defaultS3HealthInterval = 30 * time.Second

	// ReadyProbeName is the ops-contract name for the S3 readiness probe registered
	// by Checkers(). Probe name follows the observability rule: snake_case + "_ready"
	// suffix. Single-sourced here so production code and tests reference the same
	// identifier without drift.
	ReadyProbeName = "s3_ready"
)

// bucketHeader is the narrow interface used by the health state machine.
// *awss3.Client satisfies it; tests inject a mock via newClientWithHead.
type bucketHeader interface {
	HeadBucket(ctx context.Context, params *awss3.HeadBucketInput, optFns ...func(*awss3.Options)) (*awss3.HeadBucketOutput, error)
}

// Config holds the S3 connection configuration.
type Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
	HTTPTimeout     time.Duration // default 30s
	HealthInterval  time.Duration // default 30s; background probe cadence
	// Clock is the time source for the background health ticker.
	// REQUIRED: panics via clock.MustHaveClock when nil at New().
	// Composition root passes clock.Real(); tests pass clockmock.New(...).
	Clock clock.Clock
}

// ConfigFromEnv creates a Config from environment variables.
//
// The returned Config.Clock field is zero (nil); callers MUST set Clock
// before calling New() because s3.New panics via clock.MustHaveClock when
// nil. Composition root: pass clock.Real(); tests: pass clockmock.New(...).
func ConfigFromEnv() Config {
	return Config{
		Endpoint:        envWithFallback("GOCELL_S3_ENDPOINT", "S3_ENDPOINT"),
		Region:          envWithFallback("GOCELL_S3_REGION", "S3_REGION"),
		Bucket:          envWithFallback("GOCELL_S3_BUCKET", "S3_BUCKET"),
		AccessKeyID:     envWithFallback("GOCELL_S3_ACCESS_KEY", "S3_ACCESS_KEY_ID"),
		SecretAccessKey: envWithFallback("GOCELL_S3_SECRET_KEY", "S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    envWithFallback("GOCELL_S3_USE_PATH_STYLE", "S3_USE_PATH_STYLE") == "true",
		HTTPTimeout:     defaultS3HTTPTimeout,
	}
}

func envWithFallback(primary, legacy string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	if v := os.Getenv(legacy); v != "" {
		slog.Warn("deprecated S3_* env vars used, migrate to GOCELL_S3_*",
			slog.String("var", legacy))
		return v
	}
	return ""
}

// Validate checks that required fields are populated and that the endpoint
// uses a TLS scheme for remote hosts (loopback exempt for dev/CI).
func (c Config) Validate() error {
	if c.Endpoint == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterS3Config, "s3: endpoint is required")
	}
	// SEC-FAIL-CLOSED: reject non-TLS endpoints before any network operation.
	if err := secutil.ValidateTLSEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.Region == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterS3Config, "s3: region is required")
	}
	if c.Bucket == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterS3Config, "s3: bucket is required")
	}
	if c.AccessKeyID == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterS3Config, "s3: access key ID is required")
	}
	if c.SecretAccessKey == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterS3Config, "s3: secret access key is required")
	}
	return nil
}

// Client is a thin S3 adapter backed by aws-sdk-go-v2.
// It implements lifecycle.ManagedResource: Checkers() reads the last known
// health state without network I/O; Worker() drives a background ticker that
// re-probes HeadBucket on each tick and updates state.
//
// ref: runtime/websocket/hub.go — state-machine + worker adapter pattern.
// ref: kubernetes/kubernetes pkg/util/healthz — named health checkers.
type Client struct {
	config Config
	clk    clock.Clock   // injected time source; required (validated via clock.MustHaveClock)
	s3     *awss3.Client // full SDK client; used for Upload, SDK(), and Health()
	head   bucketHeader  // narrow interface for state-machine probes; equals s3 in production

	// state holds the latest HeadBucket result.
	// nil pointer = healthy; non-nil pointer to non-nil error = unhealthy.
	state atomic.Pointer[error]

	// started is set to true at the very beginning of runHealthLoop so that
	// Close and Stop can skip the workerDone drain when the worker goroutine
	// was never started (e.g. bootstrap aborted before Worker.Start).
	started atomic.Bool

	stopOnce   sync.Once
	stopCh     chan struct{} // signals the worker goroutine to exit
	workerDone chan struct{} // closed when the worker goroutine returns
}

// Compile-time assertion: *Client satisfies lifecycle.ManagedResource.
var _ lifecycle.ManagedResource = (*Client)(nil)

// New creates a Client with ctx and cfg. It synchronously runs one HeadBucket
// probe; failure returns a wrapped ErrAdapterS3Health error (fail-fast, symmetric
// to oidc adapter).
//
// Breaking change from previous New(cfg Config): ctx is now the first argument.
// There are zero production callers of adapters/s3 — the signature is safe to change.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = defaultS3HTTPTimeout
	}

	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "",
		),
		HTTPClient: &http.Client{Timeout: timeout},
	}

	s3Client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = cfg.UsePathStyle
	})

	return newClientWithHead(ctx, cfg, s3Client)
}

// newClientWithHead is the internal constructor shared by New and tests.
// It accepts an explicit bucketHeader so tests can inject mocks without
// going through the full AWS SDK setup. If head is a *awss3.Client, it is also
// stored as c.s3 so SDK() and Upload() work; mock implementations leave c.s3 nil.
//
// Boundary validation: cfg.Clock is required (panics via clock.MustHaveClock
// when nil or typed-nil). The composition root injects clock.Real(); tests
// inject clockmock.New(...).
// ref: KERNEL-CLOCK-LEAF-FALLBACK-01 — production leaves must not fallback to
// clock.Real(); only the composition root may construct it.
func newClientWithHead(ctx context.Context, cfg Config, head bucketHeader) (*Client, error) {
	clock.MustHaveClock(cfg.Clock, "s3.New")
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = defaultS3HealthInterval
	}

	c := &Client{
		config:     cfg,
		clk:        cfg.Clock,
		head:       head,
		stopCh:     make(chan struct{}),
		workerDone: make(chan struct{}),
	}

	// If head is the real SDK client, also wire up the full-capability field.
	if real, ok := head.(*awss3.Client); ok {
		c.s3 = real
	}

	// Sync probe: failure → construction aborts.
	if err := c.headBucket(ctx); err != nil {
		return nil, err
	}

	return c, nil
}

// headBucket calls HeadBucket and updates internal state. Returns the wrapped
// error on failure (the same error is also stored in state).
func (c *Client) headBucket(ctx context.Context) error {
	_, err := c.head.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(c.config.Bucket),
	})
	if err != nil {
		wrapped := classifyS3Error(err, ErrAdapterS3Health,
			fmt.Sprintf("bucket=%s", c.config.Bucket))
		c.state.Store(&wrapped)
		return wrapped
	}
	c.state.Store(nil)
	return nil
}

// SDK returns the underlying aws-sdk-go-v2 S3 client for operations not
// covered by this thin adapter (download, delete, presigned URLs, etc.).
func (c *Client) SDK() *awss3.Client { return c.s3 }

// Upload stores an object via PutObject. Used by cells implementing object archival.
//
// Requires a full SDK client (c.s3 non-nil). When constructed via newClientWithHead
// with a mock (tests), c.s3 is nil and Upload returns ErrAdapterS3Upload rather
// than panicking on nil dereference.
func (c *Client) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	if c.s3 == nil {
		return errcode.New(errcode.KindInternal, ErrAdapterS3Upload,
			"s3: full SDK client unavailable; Upload requires production s3.Client construction")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := c.s3.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(c.config.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return classifyS3Error(err, ErrAdapterS3Upload,
			fmt.Sprintf("key=%s", key))
	}
	slog.Debug("s3: object uploaded", slog.String("key", key), slog.Int("size", len(data)))
	return nil
}

// Health checks bucket accessibility via a direct HeadBucket network call.
// This is useful for one-shot diagnostics. The background worker and Checkers
// use the internal state, not this method.
//
// Requires a full SDK client (c.s3 non-nil). When constructed via newClientWithHead
// with a mock (tests), c.s3 is nil and Health returns ErrAdapterS3Health rather
// than panicking on nil dereference.
func (c *Client) Health(ctx context.Context) error {
	if c.s3 == nil {
		return errcode.New(errcode.KindInternal, ErrAdapterS3Health,
			"s3: full SDK client unavailable; Health requires production s3.Client construction")
	}
	_, err := c.s3.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(c.config.Bucket),
	})
	if err != nil {
		return classifyS3Error(err, ErrAdapterS3Health,
			fmt.Sprintf("bucket=%s", c.config.Bucket))
	}
	return nil
}

// ---------------------------------------------------------------------------
// lifecycle.ManagedResource implementation
// ---------------------------------------------------------------------------

// Checkers implements lifecycle.ManagedResource. It returns a single probe
// keyed by ReadyProbeName ("s3_ready") that reads the latest health state
// without any network call.
//
// probe name follows the observability rule: snake_case + "_ready" suffix.
// ReadyProbeName is the single source of truth; both this method and tests
// reference it so the name cannot drift.
//
// ref: runtime/websocket/hub.go Checkers — state-read probe pattern.
func (c *Client) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		ReadyProbeName: func(_ context.Context) error {
			if errPtr := c.state.Load(); errPtr != nil {
				return *errPtr
			}
			return nil
		},
	}
}

// s3HealthWorker adapts *Client to the kernel/worker.Worker contract so that
// bootstrap.WithManagedResource(client) auto-starts the health loop via WorkerGroup.
//
// Stop is idempotent: calling Stop multiple times is safe (signalStop uses sync.Once).
//
// ref: runtime/websocket/hub.go hubWorker — same worker-adapter pattern.
type s3HealthWorker struct{ c *Client }

// Compile-time assertion: s3HealthWorker satisfies kernel/worker.Worker.
var _ worker.Worker = (*s3HealthWorker)(nil)

func (w *s3HealthWorker) Start(ctx context.Context) error {
	w.c.runHealthLoop(ctx)
	return nil
}

func (w *s3HealthWorker) Stop(ctx context.Context) error {
	w.c.signalStop()
	// Fast path: worker goroutine never started — nothing to drain.
	if !w.c.started.Load() {
		return nil
	}
	select {
	case <-w.c.workerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Worker returns a worker.Worker that drives the background health probe loop.
//
// ref: runtime/websocket/hub.go Worker — non-nil documents "auto-managed background worker".
func (c *Client) Worker() worker.Worker { return &s3HealthWorker{c: c} }

// signalStop closes stopCh exactly once (idempotent via sync.Once).
func (c *Client) signalStop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
}

// runHealthLoop is the background goroutine body. It ticks every
// Config.HealthInterval, calls headBucket to update state, and exits when
// either ctx is done or stopCh is closed. It closes workerDone on exit.
func (c *Client) runHealthLoop(ctx context.Context) {
	c.started.Store(true)
	defer close(c.workerDone)

	ticker := c.clk.NewTicker(c.config.HealthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C():
			if err := c.headBucket(ctx); err != nil {
				slog.Warn("s3: health probe failed; state marked unhealthy",
					slog.String("bucket", c.config.Bucket),
					slog.Any("error", err))
			} else {
				slog.Debug("s3: health probe succeeded", slog.String("bucket", c.config.Bucket))
			}
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Close implements lifecycle.ManagedResource. It signals the worker to stop
// and waits for the goroutine to drain, bounded by ctx. Idempotent — safe to
// call from both Worker.Stop and ManagedResource.Close teardown paths.
//
// Fast path: if the worker goroutine was never started (e.g. bootstrap aborted
// before Worker.Start was called), workerDone will never be closed. In that
// case we skip the select so we do not burn the shutdown budget on a
// non-existent drain.
//
// ref: runtime/websocket/hub.go Close — idempotent + ctx-bounded pattern.
func (c *Client) Close(ctx context.Context) error {
	c.signalStop()
	// Fast path: worker goroutine never started — nothing to drain.
	if !c.started.Load() {
		return nil
	}
	select {
	case <-c.workerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
