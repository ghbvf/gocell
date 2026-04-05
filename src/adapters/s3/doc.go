// Package s3 provides a generic S3-compatible object storage adapter for GoCell.
//
// It implements Upload, Download, Delete, and Presigned URL operations using
// the AWS S3 REST API with Signature V4 authentication. The adapter is compatible
// with both AWS S3 and MinIO endpoints.
//
// This package provides a general-purpose ObjectStore capability. Cell-specific
// abstractions (e.g., ArchiveStore) should be built on top of this adapter
// within the respective Cell's internal packages.
//
// Configuration can be provided programmatically via [Config] or loaded from
// environment variables via [ConfigFromEnv]:
//
//	GOCELL_S3_ENDPOINT   — S3/MinIO endpoint (e.g., "localhost:9000")
//	GOCELL_S3_ACCESS_KEY — Access key ID
//	GOCELL_S3_SECRET_KEY — Secret access key
//	GOCELL_S3_BUCKET     — Default bucket name
//	GOCELL_S3_USE_SSL    — Use HTTPS ("true" or "false", default "false")
//	GOCELL_S3_REGION     — AWS region (default "us-east-1")
package s3
