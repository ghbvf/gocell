package s3

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/secutil"
)

const (
	// defaultS3HTTPTimeout is the default HTTP client timeout for S3 operations.
	defaultS3HTTPTimeout = 30 * time.Second
)

// Config holds the S3 connection configuration.
type Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
	HTTPTimeout     time.Duration // default 30s
}

// ConfigFromEnv creates a Config from environment variables.
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
		return errcode.New(ErrAdapterS3Config, "s3: endpoint is required")
	}
	// SEC-FAIL-CLOSED: reject non-TLS endpoints before any network operation.
	if err := secutil.ValidateTLSEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.Region == "" {
		return errcode.New(ErrAdapterS3Config, "s3: region is required")
	}
	if c.Bucket == "" {
		return errcode.New(ErrAdapterS3Config, "s3: bucket is required")
	}
	if c.AccessKeyID == "" {
		return errcode.New(ErrAdapterS3Config, "s3: access key ID is required")
	}
	if c.SecretAccessKey == "" {
		return errcode.New(ErrAdapterS3Config, "s3: secret access key is required")
	}
	return nil
}

// Client is a thin S3 adapter backed by aws-sdk-go-v2.
type Client struct {
	config Config
	s3     *awss3.Client
}

// New creates a Client. For advanced SDK usage, access the underlying
// aws-sdk-go-v2 client via SDK().
func New(cfg Config) (*Client, error) {
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

	return &Client{config: cfg, s3: s3Client}, nil
}

// SDK returns the underlying aws-sdk-go-v2 S3 client for operations not
// covered by this thin adapter (download, delete, presigned URLs, etc.).
func (c *Client) SDK() *awss3.Client { return c.s3 }

// Upload stores an object. Implements the ObjectUploader interface used
// by cells/auditcore/s3archive.
func (c *Client) Upload(ctx context.Context, key string, data []byte, contentType string) error {
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
		return errcode.Wrap(ErrAdapterS3Upload,
			fmt.Sprintf("s3: upload failed for key %s", key), err)
	}
	slog.Debug("s3: object uploaded", slog.String("key", key), slog.Int("size", len(data)))
	return nil
}

// Health checks bucket accessibility via HeadBucket.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.s3.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(c.config.Bucket),
	})
	if err != nil {
		return errcode.Wrap(ErrAdapterS3Health,
			fmt.Sprintf("s3: health check failed for bucket %s", c.config.Bucket), err)
	}
	return nil
}
