// Package s3 provides an S3-compatible object storage adapter for the GoCell
// framework.
//
// It implements the blob storage interfaces defined in kernel/ and runtime/,
// providing object upload, download, deletion, listing, and presigned URL
// generation. Compatible with AWS S3, MinIO, and other S3-compatible services.
//
// # Configuration
//
//	Endpoint:        "https://s3.amazonaws.com"  (or MinIO URL)
//	Region:          "us-east-1"
//	Bucket:          "gocell-assets"
//	AccessKeyID:     "<key>"
//	SecretAccessKey:  "<secret>"
//	UsePathStyle:    false  (set true for MinIO)
//
// # Presigned URLs
//
// The adapter generates presigned URLs for temporary read/write access,
// useful for direct browser uploads or audit log archive downloads.
//
// # Close
//
// Always call Close to release the HTTP client resources.
package s3
