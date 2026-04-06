package s3

import (
	"bytes"
	"context"
	"fmt"
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
