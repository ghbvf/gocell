package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

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
//
//	S3_ENDPOINT, S3_REGION, S3_BUCKET, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY
func ConfigFromEnv() Config {
	return Config{
		Endpoint:       os.Getenv("S3_ENDPOINT"),
		Region:         os.Getenv("S3_REGION"),
		Bucket:         os.Getenv("S3_BUCKET"),
		AccessKeyID:    os.Getenv("S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		UsePathStyle:   os.Getenv("S3_USE_PATH_STYLE") == "true",
		HTTPTimeout:    30 * time.Second,
	}
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

// Client is an S3-compatible object storage client.
type Client struct {
	config Config
	http   *http.Client
}

// New creates a new S3 Client.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		config: cfg,
		http:   &http.Client{Timeout: timeout},
	}, nil
}

// Health checks if the S3 bucket is accessible by performing a HEAD request.
func (c *Client) Health(ctx context.Context) error {
	reqURL := c.bucketURL("")
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, reqURL, nil)
	if err != nil {
		return errcode.Wrap(ErrAdapterS3Health, "s3: failed to create health request", err)
	}

	c.signRequest(req, nil)

	resp, err := c.http.Do(req)
	if err != nil {
		return errcode.Wrap(ErrAdapterS3Health, "s3: health check request failed", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("s3: failed to close health response body",
				slog.Any("error", closeErr))
		}
	}()

	if resp.StatusCode >= 400 {
		return errcode.New(ErrAdapterS3Health,
			fmt.Sprintf("s3: health check returned status %d", resp.StatusCode))
	}

	return nil
}

// bucketURL returns the full URL for the given object key.
func (c *Client) bucketURL(key string) string {
	endpoint := strings.TrimRight(c.config.Endpoint, "/")
	if c.config.UsePathStyle {
		if key == "" {
			return fmt.Sprintf("%s/%s", endpoint, c.config.Bucket)
		}
		return fmt.Sprintf("%s/%s/%s", endpoint, c.config.Bucket, key)
	}
	// Virtual-hosted style.
	if key == "" {
		return endpoint
	}
	return fmt.Sprintf("%s/%s", endpoint, key)
}

// signRequest signs an HTTP request with AWS Signature V4.
func (c *Client) signRequest(req *http.Request, payload []byte) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", hashSHA256(payload))

	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}

	// Canonical request components.
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQueryString := req.URL.Query().Encode()

	signedHeaders, canonicalHeaders := buildCanonicalHeaders(req)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		hashSHA256(payload),
	}, "\n")

	// String to sign.
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.config.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hashSHA256([]byte(canonicalRequest)),
	}, "\n")

	// Signing key.
	signingKey := deriveSigningKey(c.config.SecretAccessKey, dateStamp, c.config.Region, "s3")

	// Signature.
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Authorization header.
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.config.AccessKeyID, scope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)
}

// buildCanonicalHeaders builds the canonical header string and signed header list.
func buildCanonicalHeaders(req *http.Request) (signedHeaders, canonicalHeaders string) {
	headers := make(map[string]string)
	var keys []string

	for key := range req.Header {
		lk := strings.ToLower(key)
		if lk == "host" || strings.HasPrefix(lk, "x-amz-") || lk == "content-type" {
			headers[lk] = strings.TrimSpace(req.Header.Get(key))
			keys = append(keys, lk)
		}
	}

	sort.Strings(keys)

	var headerParts []string
	for _, k := range keys {
		headerParts = append(headerParts, k+":"+headers[k]+"\n")
	}

	return strings.Join(keys, ";"), strings.Join(headerParts, "")
}

// hashSHA256 returns the hex-encoded SHA-256 hash of data.
func hashSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// hmacSHA256 computes HMAC-SHA256.
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// deriveSigningKey derives the AWS Signature V4 signing key.
func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// doRequest executes a signed request and returns the response body.
func (c *Client) doRequest(ctx context.Context, method, url string, body io.Reader, payload []byte, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterS3Upload, "s3: create request", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	c.signRequest(req, payload)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterS3Upload, "s3: request failed", err)
	}

	return resp, nil
}
