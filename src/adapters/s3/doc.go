// Package s3 provides an S3-compatible object storage adapter for the GoCell
// framework. It implements upload, download, delete, and presigned URL
// generation using AWS Signature V4 authentication without external S3 libraries.
//
// ref: aws/aws-sdk-go-v2 service/s3 — API design, Signature V4
// Adopted: Signature V4 signing, standard S3 REST API paths.
// Deviated: no external SDK dependency; minimal stdlib-only implementation
// sufficient for GoCell's storage requirements.
package s3
