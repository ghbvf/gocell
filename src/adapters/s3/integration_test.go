//go:build integration

// Package s3 provides the S3-compatible object storage adapter for GoCell.
// Integration tests require a running S3-compatible service (e.g. MinIO).
package s3

import "testing"

// TestIntegration_PutGetObject verifies upload and download of an object.
func TestIntegration_PutGetObject(t *testing.T) {
	t.Skip("stub: requires running S3-compatible service")
}

// TestIntegration_DeleteObject verifies object deletion.
func TestIntegration_DeleteObject(t *testing.T) {
	t.Skip("stub: requires running S3-compatible service")
}

// TestIntegration_ListObjects verifies listing objects with prefix filtering.
func TestIntegration_ListObjects(t *testing.T) {
	t.Skip("stub: requires running S3-compatible service")
}

// TestIntegration_PresignedURL verifies generation of presigned URLs for temporary access.
func TestIntegration_PresignedURL(t *testing.T) {
	t.Skip("stub: requires running S3-compatible service")
}

// TestIntegration_Close verifies graceful shutdown of the S3 client.
func TestIntegration_Close(t *testing.T) {
	t.Skip("stub: requires running S3-compatible service")
}
