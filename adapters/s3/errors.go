package s3

import "github.com/ghbvf/gocell/pkg/errcode"

// S3 adapter error codes.
const (
	ErrAdapterS3Config errcode.Code = "ERR_ADAPTER_S3_CONFIG"
	ErrAdapterS3Upload errcode.Code = "ERR_ADAPTER_S3_UPLOAD"
	ErrAdapterS3Health errcode.Code = "ERR_ADAPTER_S3_HEALTH"
)
