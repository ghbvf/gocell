package s3

import "github.com/ghbvf/gocell/pkg/errcode"

// S3 adapter error codes.
const (
	ErrS3Upload   errcode.Code = "ERR_ADAPTER_S3_UPLOAD"
	ErrS3Download errcode.Code = "ERR_ADAPTER_S3_DOWNLOAD"
	ErrS3NotFound errcode.Code = "ERR_ADAPTER_S3_NOT_FOUND"
	ErrS3Delete   errcode.Code = "ERR_ADAPTER_S3_DELETE"
	ErrS3Presign  errcode.Code = "ERR_ADAPTER_S3_PRESIGN"
	ErrS3Health   errcode.Code = "ERR_ADAPTER_S3_HEALTH"
	ErrS3Config   errcode.Code = "ERR_ADAPTER_S3_CONFIG"
)
