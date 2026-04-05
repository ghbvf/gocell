package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Upload stores an object with the given key and content.
func (c *Client) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	url := c.bucketURL(key)

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	resp, err := c.doRequest(ctx, "PUT", url, bytes.NewReader(data), data, contentType)
	if err != nil {
		return errcode.Wrap(ErrAdapterS3Upload,
			fmt.Sprintf("s3: upload failed for key %s", key), err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("s3: failed to close upload response body",
				slog.Any("error", closeErr))
		}
	}()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return errcode.New(ErrAdapterS3Upload,
			fmt.Sprintf("s3: upload returned status %d for key %s: %s",
				resp.StatusCode, key, string(body)))
	}

	slog.Debug("s3: object uploaded",
		slog.String("key", key),
		slog.Int("size", len(data)),
	)

	return nil
}

// Download retrieves an object by key.
func (c *Client) Download(ctx context.Context, key string) ([]byte, error) {
	url := c.bucketURL(key)

	resp, err := c.doRequest(ctx, "GET", url, nil, nil, "")
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterS3Download,
			fmt.Sprintf("s3: download failed for key %s", key), err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("s3: failed to close download response body",
				slog.Any("error", closeErr))
		}
	}()

	if resp.StatusCode >= 400 {
		return nil, errcode.New(ErrAdapterS3Download,
			fmt.Sprintf("s3: download returned status %d for key %s",
				resp.StatusCode, key))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterS3Download,
			fmt.Sprintf("s3: failed to read response for key %s", key), err)
	}

	return data, nil
}

// Delete removes an object by key.
func (c *Client) Delete(ctx context.Context, key string) error {
	url := c.bucketURL(key)

	resp, err := c.doRequest(ctx, "DELETE", url, nil, nil, "")
	if err != nil {
		return errcode.Wrap(ErrAdapterS3Delete,
			fmt.Sprintf("s3: delete failed for key %s", key), err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("s3: failed to close delete response body",
				slog.Any("error", closeErr))
		}
	}()

	// S3 returns 204 for successful deletes; 404 is also acceptable (idempotent).
	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		return errcode.New(ErrAdapterS3Delete,
			fmt.Sprintf("s3: delete returned status %d for key %s",
				resp.StatusCode, key))
	}

	slog.Debug("s3: object deleted",
		slog.String("key", key),
	)

	return nil
}
