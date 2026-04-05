package s3

import "github.com/ghbvf/gocell/pkg/errcode"

// S3 adapter error codes.
const (
	// ErrAdapterS3Config indicates an invalid S3 configuration.
	ErrAdapterS3Config errcode.Code = "ERR_ADAPTER_S3_CONFIG"
	// ErrAdapterS3Upload indicates a failure uploading an object.
	ErrAdapterS3Upload errcode.Code = "ERR_ADAPTER_S3_UPLOAD"
	// ErrAdapterS3Download indicates a failure downloading an object.
	ErrAdapterS3Download errcode.Code = "ERR_ADAPTER_S3_DOWNLOAD"
	// ErrAdapterS3Delete indicates a failure deleting an object.
	ErrAdapterS3Delete errcode.Code = "ERR_ADAPTER_S3_DELETE"
	// ErrAdapterS3Presign indicates a failure generating a presigned URL.
	ErrAdapterS3Presign errcode.Code = "ERR_ADAPTER_S3_PRESIGN"
	// ErrAdapterS3Health indicates an S3 health check failure.
	ErrAdapterS3Health errcode.Code = "ERR_ADAPTER_S3_HEALTH"
)
