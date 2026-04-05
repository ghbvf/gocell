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

// PresignedGet generates a presigned GET URL for downloading an object.
func (c *Client) PresignedGet(key string, ttl time.Duration) (string, error) {
	return c.presignURL("GET", key, ttl)
}

// PresignedPut generates a presigned PUT URL for uploading an object.
func (c *Client) PresignedPut(key string, ttl time.Duration) (string, error) {
	return c.presignURL("PUT", key, ttl)
}

// presignURL generates a presigned URL using AWS Signature V4 query string auth.
func (c *Client) presignURL(method, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errcode.New(ErrAdapterS3Presign, "s3: TTL must be positive")
	}
	if ttl > 7*24*time.Hour {
		return "", errcode.New(ErrAdapterS3Presign, "s3: TTL cannot exceed 7 days")
	}

	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.config.Region)

	rawURL := c.bucketURL(key)
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", errcode.Wrap(ErrAdapterS3Presign, "s3: failed to parse URL", err)
	}

	canonicalURI := parsedURL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	// Build the signed headers — for presigned URLs, only host is signed.
	signedHeaders := "host"
	canonicalHeaders := "host:" + parsedURL.Host + "\n"

	// Query parameters for presigned URL.
	queryParams := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {c.config.AccessKeyID + "/" + scope},
		"X-Amz-Date":          {amzDate},
		"X-Amz-Expires":       {fmt.Sprintf("%d", int(ttl.Seconds()))},
		"X-Amz-SignedHeaders":  {signedHeaders},
	}

	// Build canonical query string (sorted).
	var sortedKeys []string
	for k := range queryParams {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	var canonicalQueryParts []string
	for _, k := range sortedKeys {
		canonicalQueryParts = append(canonicalQueryParts,
			url.QueryEscape(k)+"="+url.QueryEscape(queryParams.Get(k)))
	}
	canonicalQueryString := strings.Join(canonicalQueryParts, "&")

	// For presigned URLs, the payload hash is UNSIGNED-PAYLOAD.
	payloadHash := "UNSIGNED-PAYLOAD"

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign.
	h := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(h[:]),
	}, "\n")

	// Compute signature.
	signingKey := deriveSigningKey(c.config.SecretAccessKey, dateStamp, c.config.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Build final URL.
	presignedURL := fmt.Sprintf("%s://%s%s?%s&X-Amz-Signature=%s",
		parsedURL.Scheme, parsedURL.Host, canonicalURI,
		canonicalQueryString, signature)

	return presignedURL, nil
}
