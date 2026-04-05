//go:build integration

// Package s3_test contains integration tests for the S3-compatible adapter.
// These tests require a running MinIO or LocalStack instance (via Docker/testcontainers).
package s3_test

import "testing"

// TestIntegration_S3Connection verifies basic connection and bucket
// operations against an S3-compatible service.
func TestIntegration_S3Connection(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup (MinIO)
	// 1. Start MinIO container
	// 2. Create test bucket
	// 3. Verify bucket exists
	// 4. Verify health check
}

// TestIntegration_S3PutGetDelete verifies basic object lifecycle.
func TestIntegration_S3PutGetDelete(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup
	// 1. PutObject with content
	// 2. GetObject and verify content matches
	// 3. DeleteObject
	// 4. Verify GetObject returns not-found
}

// TestIntegration_S3AuditArchive verifies the audit log archive flow
// used by audit-core for long-term storage.
func TestIntegration_S3AuditArchive(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup
	// 1. Write audit batch as gzipped JSON
	// 2. Verify object metadata (content-type, timestamp)
	// 3. Read back and decompress
	// 4. Verify audit entries match original
}

// TestIntegration_S3PresignedURL verifies presigned URL generation
// for temporary access.
func TestIntegration_S3PresignedURL(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup
	// 1. PutObject
	// 2. Generate presigned GET URL with 5m expiry
	// 3. HTTP GET the presigned URL, verify content
}

// TestIntegration_S3LargeObject verifies multipart upload for large objects.
func TestIntegration_S3LargeObject(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup
	// 1. Generate 10MB test payload
	// 2. Upload via multipart
	// 3. Download and verify integrity (checksum)
}
