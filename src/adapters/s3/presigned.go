package s3

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// defaultPresignTTL is the default expiration duration for presigned URLs.
const defaultPresignTTL = 15 * time.Minute

// maxPresignTTL is the maximum allowed TTL for presigned URLs (7 days, AWS limit).
const maxPresignTTL = 7 * 24 * time.Hour

// PresignedGetURL generates a presigned URL for downloading an object via HTTP GET.
// If ttl is zero, defaultPresignTTL (15 minutes) is used.
func (c *Client) PresignedGetURL(key string, ttl time.Duration) (string, *errcode.Error) {
	return c.presignURL(key, "GET", ttl)
}

// PresignedPutURL generates a presigned URL for uploading an object via HTTP PUT.
// If ttl is zero, defaultPresignTTL (15 minutes) is used.
func (c *Client) PresignedPutURL(key string, ttl time.Duration) (string, *errcode.Error) {
	return c.presignURL(key, "PUT", ttl)
}

// presignURL generates a presigned URL using AWS Signature V4 query string authentication.
func (c *Client) presignURL(key, method string, ttl time.Duration) (string, *errcode.Error) {
	if ttl == 0 {
		ttl = defaultPresignTTL
	}
	if ttl > maxPresignTTL {
		return "", errcode.WithDetails(
			errcode.New(ErrS3Presign, "s3: presigned URL TTL exceeds maximum"),
			map[string]any{"ttl": ttl.String(), "max": maxPresignTTL.String()},
		)
	}

	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", datestamp, c.cfg.Region)
	credential := fmt.Sprintf("%s/%s", c.cfg.AccessKey, credentialScope)

	objURL := c.objectURL(key)
	parsed, err := url.Parse(objURL)
	if err != nil {
		return "", errcode.Wrap(ErrS3Presign, "s3: failed to parse object URL", err)
	}

	// Build the canonical query string with AWS auth parameters.
	// The "host" header is the only signed header for presigned URLs.
	q := parsed.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", credential)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(ttl.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")

	// Build canonical query string (sorted)
	canonicalQueryString := buildSortedQueryString(q)

	// Canonical URI
	canonicalURI := parsed.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	// For presigned URLs, the payload hash is "UNSIGNED-PAYLOAD"
	payloadHash := "UNSIGNED-PAYLOAD"

	// Canonical headers for presigned URL: only "host"
	canonicalHeaders := fmt.Sprintf("host:%s\n", parsed.Host)

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		"host",
		payloadHash,
	}, "\n")

	// String to sign
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hashSHA256(canonicalRequest),
	}, "\n")

	// Signing key
	signingKey := deriveSigningKey(c.cfg.SecretKey, datestamp, c.cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Append signature to query string
	q.Set("X-Amz-Signature", signature)
	parsed.RawQuery = q.Encode()

	return parsed.String(), nil
}

// buildSortedQueryString encodes query parameters in canonical sorted order.
func buildSortedQueryString(params url.Values) string {
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

// hashSHA256 returns the lowercase hex-encoded SHA-256 hash of a string.
func hashSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
