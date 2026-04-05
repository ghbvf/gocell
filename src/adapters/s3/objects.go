package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// UploadInput describes the object to upload.
type UploadInput struct {
	Key         string
	Body        io.Reader
	ContentType string // optional, defaults to "application/octet-stream"
	Size        int64  // content length; required for signing
}

// DownloadOutput holds the result of a Download operation.
type DownloadOutput struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
}

// Upload stores an object in the configured bucket.
func (c *Client) Upload(ctx context.Context, in UploadInput) *errcode.Error {
	if in.ContentType == "" {
		in.ContentType = "application/octet-stream"
	}

	payload, err := io.ReadAll(in.Body)
	if err != nil {
		return errcode.Wrap(ErrS3Upload, "s3: failed to read upload body", err)
	}

	reqURL := c.objectURL(in.Key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(payload))
	if err != nil {
		return errcode.Wrap(ErrS3Upload, "s3: failed to create upload request", err)
	}

	req.Header.Set("Content-Type", in.ContentType)
	req.ContentLength = int64(len(payload))

	c.signV4(req, payload)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errcode.Wrap(ErrS3Upload, "s3: upload request failed", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("s3: failed to close upload response body", "error", cerr, "key", in.Key)
		}
	}()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return errcode.WithDetails(
			errcode.New(ErrS3Upload, fmt.Sprintf("s3: upload failed with status %d", resp.StatusCode)),
			map[string]any{"key": in.Key, "statusCode": resp.StatusCode, "response": string(body)},
		)
	}

	slog.Info("s3: object uploaded", "key", in.Key, "bucket", c.cfg.Bucket, "size", len(payload))
	return nil
}

// Download retrieves an object from the configured bucket.
// The caller is responsible for closing DownloadOutput.Body.
func (c *Client) Download(ctx context.Context, key string) (*DownloadOutput, *errcode.Error) {
	reqURL := c.objectURL(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, errcode.Wrap(ErrS3Download, "s3: failed to create download request", err)
	}

	c.signV4(req, nil)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errcode.Wrap(ErrS3Download, "s3: download request failed", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("s3: failed to close 404 response body", "error", cerr, "key", key)
		}
		return nil, errcode.WithDetails(
			errcode.New(ErrS3NotFound, "s3: object not found"),
			map[string]any{"key": key, "bucket": c.cfg.Bucket},
		)
	}

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("s3: failed to close error response body", "error", cerr, "key", key)
		}
		return nil, errcode.WithDetails(
			errcode.New(ErrS3Download, fmt.Sprintf("s3: download failed with status %d", resp.StatusCode)),
			map[string]any{"key": key, "statusCode": resp.StatusCode, "response": string(body)},
		)
	}

	return &DownloadOutput{
		Body:        resp.Body,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        resp.ContentLength,
	}, nil
}

// Delete removes an object from the configured bucket.
func (c *Client) Delete(ctx context.Context, key string) *errcode.Error {
	reqURL := c.objectURL(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return errcode.Wrap(ErrS3Delete, "s3: failed to create delete request", err)
	}

	c.signV4(req, nil)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errcode.Wrap(ErrS3Delete, "s3: delete request failed", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("s3: failed to close delete response body", "error", cerr, "key", key)
		}
	}()

	// S3 returns 204 No Content on successful delete. It also returns 204
	// for keys that do not exist (idempotent delete).
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return errcode.WithDetails(
			errcode.New(ErrS3Delete, fmt.Sprintf("s3: delete failed with status %d", resp.StatusCode)),
			map[string]any{"key": key, "statusCode": resp.StatusCode, "response": string(body)},
		)
	}

	slog.Info("s3: object deleted", "key", key, "bucket", c.cfg.Bucket)
	return nil
}
