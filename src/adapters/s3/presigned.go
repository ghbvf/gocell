package s3

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// PresignedGet generates a presigned GET URL for downloading an object.
func (c *Client) PresignedGet(key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errcode.New(ErrAdapterS3Presign, "s3: TTL must be positive")
	}
	if ttl > 7*24*time.Hour {
		return "", errcode.New(ErrAdapterS3Presign, "s3: TTL cannot exceed 7 days")
	}

	resp, err := c.presign.PresignGetObject(context.Background(),
		&awss3.GetObjectInput{
			Bucket: aws.String(c.config.Bucket),
			Key:    aws.String(key),
		},
		func(o *awss3.PresignOptions) {
			o.Expires = ttl
		},
	)
	if err != nil {
		return "", errcode.Wrap(ErrAdapterS3Presign,
			fmt.Sprintf("s3: presigned GET failed for key %s", key), err)
	}

	return resp.URL, nil
}

// PresignedPut generates a presigned PUT URL for uploading an object.
func (c *Client) PresignedPut(key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errcode.New(ErrAdapterS3Presign, "s3: TTL must be positive")
	}
	if ttl > 7*24*time.Hour {
		return "", errcode.New(ErrAdapterS3Presign, "s3: TTL cannot exceed 7 days")
	}

	resp, err := c.presign.PresignPutObject(context.Background(),
		&awss3.PutObjectInput{
			Bucket: aws.String(c.config.Bucket),
			Key:    aws.String(key),
		},
		func(o *awss3.PresignOptions) {
			o.Expires = ttl
		},
	)
	if err != nil {
		return "", errcode.Wrap(ErrAdapterS3Presign,
			fmt.Sprintf("s3: presigned PUT failed for key %s", key), err)
	}

	return resp.URL, nil
}
