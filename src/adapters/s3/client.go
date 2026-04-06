package s3

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Config holds the S3 connection configuration.
type Config struct {
	// Endpoint is the S3-compatible endpoint URL (e.g., http://localhost:9000).
	Endpoint string
	// Region is the AWS region (e.g., us-east-1).
	Region string
	// Bucket is the default bucket name.
	Bucket string
	// AccessKeyID is the AWS access key.
	AccessKeyID string
	// SecretAccessKey is the AWS secret key.
	SecretAccessKey string
	// UsePathStyle forces path-style addressing (required for MinIO and local S3).
	UsePathStyle bool
	// HTTPTimeout is the HTTP client timeout. Default: 30 seconds.
	HTTPTimeout time.Duration
}

// ConfigFromEnv creates a Config from environment variables.
// It first tries GOCELL_S3_* prefixed variables, then falls back to the
// legacy S3_* prefix (logging a deprecation warning for each fallback).
//
//	Primary:  GOCELL_S3_ENDPOINT, GOCELL_S3_REGION, GOCELL_S3_BUCKET,
//	          GOCELL_S3_ACCESS_KEY, GOCELL_S3_SECRET_KEY, GOCELL_S3_USE_PATH_STYLE
//	Fallback: S3_ENDPOINT, S3_REGION, S3_BUCKET,
//	          S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_USE_PATH_STYLE
func ConfigFromEnv() Config {
	return Config{
		Endpoint:       envWithFallback("GOCELL_S3_ENDPOINT", "S3_ENDPOINT"),
		Region:         envWithFallback("GOCELL_S3_REGION", "S3_REGION"),
		Bucket:         envWithFallback("GOCELL_S3_BUCKET", "S3_BUCKET"),
		AccessKeyID:    envWithFallback("GOCELL_S3_ACCESS_KEY", "S3_ACCESS_KEY_ID"),
		SecretAccessKey: envWithFallback("GOCELL_S3_SECRET_KEY", "S3_SECRET_ACCESS_KEY"),
		UsePathStyle:   envWithFallback("GOCELL_S3_USE_PATH_STYLE", "S3_USE_PATH_STYLE") == "true",
		HTTPTimeout:    30 * time.Second,
	}
}

// envWithFallback reads the primary env var; if empty, falls back to the
// legacy var and emits a deprecation warning via slog.Warn.
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

// Validate checks that required Config fields are populated.
func (c Config) Validate() error {
	if c.Endpoint == "" {
		return errcode.New(ErrAdapterS3Config, "s3: endpoint is required")
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

// Client is an S3-compatible object storage client backed by aws-sdk-go-v2.
type Client struct {
	config  Config
	s3      *awss3.Client
	presign *awss3.PresignClient
}

// New creates a new S3 Client using aws-sdk-go-v2.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		),
	}

	s3Client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = cfg.UsePathStyle
	})

	presignClient := awss3.NewPresignClient(s3Client)

	return &Client{
		config:  cfg,
		s3:      s3Client,
		presign: presignClient,
	}, nil
}

// Health checks if the S3 bucket is accessible by performing a HeadBucket request.
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
