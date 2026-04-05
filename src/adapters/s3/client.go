package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Config holds S3/MinIO connection parameters.
type Config struct {
	Endpoint  string // e.g. "localhost:9000" or "s3.amazonaws.com"
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL   bool   // use HTTPS when true
	Region    string // AWS region, defaults to "us-east-1"
}

// ConfigFromEnv reads S3 configuration from GOCELL_S3_* environment variables.
func ConfigFromEnv() Config {
	region := os.Getenv("GOCELL_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	return Config{
		Endpoint:  os.Getenv("GOCELL_S3_ENDPOINT"),
		AccessKey: os.Getenv("GOCELL_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("GOCELL_S3_SECRET_KEY"),
		Bucket:    os.Getenv("GOCELL_S3_BUCKET"),
		UseSSL:   os.Getenv("GOCELL_S3_USE_SSL") == "true",
		Region:    region,
	}
}

// Validate checks that all required configuration fields are set.
func (c Config) Validate() *errcode.Error {
	var missing []string
	if c.Endpoint == "" {
		missing = append(missing, "Endpoint")
	}
	if c.AccessKey == "" {
		missing = append(missing, "AccessKey")
	}
	if c.SecretKey == "" {
		missing = append(missing, "SecretKey")
	}
	if c.Bucket == "" {
		missing = append(missing, "Bucket")
	}
	if len(missing) > 0 {
		return errcode.WithDetails(
			errcode.New(ErrS3Config, "missing required S3 configuration fields"),
			map[string]any{"missing": missing},
		)
	}
	return nil
}

// Client is an S3-compatible object storage client that communicates via the
// AWS S3 REST API using Signature V4 authentication.
type Client struct {
	cfg        Config
	httpClient *http.Client
	baseURL    string
}

// New creates a new S3 Client with the given configuration.
// It validates the config and returns an error if required fields are missing.
func New(cfg Config, opts ...Option) (*Client, *errcode.Error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}

	c := &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    fmt.Sprintf("%s://%s", scheme, cfg.Endpoint),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Option is a functional option for configuring the Client.
type Option func(*Client)

// WithHTTPClient sets a custom HTTP client for the S3 Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// Health checks connectivity to the S3 endpoint by sending a HEAD request
// to the configured bucket. It returns nil on success.
func (c *Client) Health(ctx context.Context) *errcode.Error {
	reqURL := fmt.Sprintf("%s/%s", c.baseURL, c.cfg.Bucket)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, reqURL, nil)
	if err != nil {
		return errcode.Wrap(ErrS3Health, "s3: failed to create health request", err)
	}

	c.signV4(req, nil)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errcode.Wrap(ErrS3Health, "s3: health check failed", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("s3: failed to close health response body", "error", cerr)
		}
	}()

	if resp.StatusCode >= 400 {
		return errcode.WithDetails(
			errcode.New(ErrS3Health, "s3: health check returned error status"),
			map[string]any{"statusCode": resp.StatusCode},
		)
	}
	return nil
}

// objectURL returns the full URL for an object key within the configured bucket.
func (c *Client) objectURL(key string) string {
	return fmt.Sprintf("%s/%s/%s", c.baseURL, c.cfg.Bucket, key)
}

// signV4 signs an HTTP request using AWS Signature Version 4.
func (c *Client) signV4(req *http.Request, payload []byte) {
	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("host", req.URL.Host)

	payloadHash := sha256Hex(payload)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	// Canonical request
	signedHeaders, canonicalHeaders := buildCanonicalHeaders(req)
	canonicalQueryString := buildCanonicalQueryString(req.URL)
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", datestamp, c.cfg.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// Signing key
	signingKey := deriveSigningKey(c.cfg.SecretKey, datestamp, c.cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Authorization header
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)
}

// buildCanonicalHeaders builds sorted canonical headers and signed-header list.
func buildCanonicalHeaders(req *http.Request) (signedHeaders, canonicalHeaders string) {
	headers := make(map[string]string)
	var keys []string

	for k := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			headers[lk] = strings.TrimSpace(req.Header.Get(k))
			keys = append(keys, lk)
		}
	}
	sort.Strings(keys)

	var canonical strings.Builder
	for _, k := range keys {
		canonical.WriteString(k)
		canonical.WriteString(":")
		canonical.WriteString(headers[k])
		canonical.WriteString("\n")
	}

	signedHeaders = strings.Join(keys, ";")
	canonicalHeaders = canonical.String()
	return
}

// buildCanonicalQueryString encodes and sorts query parameters.
func buildCanonicalQueryString(u *url.URL) string {
	params := u.Query()
	if len(params) == 0 {
		return ""
	}

	var keys []string
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		vals := params[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// deriveSigningKey produces the AWS Signature V4 signing key.
func deriveSigningKey(secretKey, datestamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(datestamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

// sha256Hex returns the lowercase hex-encoded SHA-256 hash of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// hmacSHA256 returns the HMAC-SHA256 of message using the given key.
func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}
