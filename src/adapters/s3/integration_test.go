//go:build integration

package s3

import (
	"testing"
)

// TestIntegration_PutGetDeleteObject uploads an object to a real
// S3-compatible store (MinIO), reads it back, then deletes it.
func TestIntegration_PutGetDeleteObject(t *testing.T) {
	t.Skip("stub: requires MinIO (docker compose up)")
}

// TestIntegration_PresignedURL generates a presigned PUT URL, uploads
// via HTTP, then verifies the object exists.
func TestIntegration_PresignedURL(t *testing.T) {
	t.Skip("stub: requires MinIO (docker compose up)")
}

// TestIntegration_ListObjects uploads multiple objects and asserts the
// list operation returns them all.
func TestIntegration_ListObjects(t *testing.T) {
	t.Skip("stub: requires MinIO (docker compose up)")
}
