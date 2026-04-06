// Package s3 provides a thin adapter over aws-sdk-go-v2 for S3-compatible
// object storage. It implements the ObjectUploader interface used by
// cells/audit-core and other consumers.
//
// This package intentionally does NOT re-export SDK types. For operations
// beyond Upload and Health (e.g., download, delete, presigned URLs), use
// the aws-sdk-go-v2 S3 client directly.
//
// ref: github.com/aws/aws-sdk-go-v2/service/s3
package s3
