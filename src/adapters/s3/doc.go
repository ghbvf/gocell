// Package s3 provides an S3-compatible object storage adapter for GoCell.
//
// This adapter implements the archive store port used by cells/audit-core for
// long-term audit record archival. It is compatible with AWS S3, MinIO, and
// any S3-protocol-compatible service.
//
// Configuration is done via S3Config, which can be populated from environment
// variables using ConfigFromEnv().
//
// # Usage
//
//	cfg := s3.ConfigFromEnv()
//	store, err := s3.New(ctx, cfg)
//	if err != nil { ... }
//
//	// Upload an object
//	err = store.Put(ctx, "audit/2026/archive.gz", data)
//
// # Environment Variables
//
// See docs/guides/adapter-config-reference.md for the full variable listing.
// Key variables: S3_ENDPOINT, S3_REGION, S3_BUCKET, S3_ACCESS_KEY_ID,
// S3_SECRET_ACCESS_KEY, S3_USE_PATH_STYLE.
//
// # Error Codes
//
// All errors use the ERR_ADAPTER_S3_* code family from pkg/errcode.
package s3
