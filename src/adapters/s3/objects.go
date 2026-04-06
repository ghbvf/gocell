package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Upload stores an object with the given key and content.
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

	slog.Debug("s3: object uploaded",
		slog.String("key", key),
		slog.Int("size", len(data)),
	)

	return nil
}

// Download retrieves an object by key.
func (c *Client) Download(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.s3.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(key),
	})
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

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterS3Download,
			fmt.Sprintf("s3: failed to read response for key %s", key), err)
	}

	return data, nil
}

// Delete removes an object by key.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return errcode.Wrap(ErrAdapterS3Delete,
			fmt.Sprintf("s3: delete failed for key %s", key), err)
	}

	slog.Debug("s3: object deleted",
		slog.String("key", key),
	)

	return nil
}
