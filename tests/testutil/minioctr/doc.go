// Package minioctr owns the MinIO testcontainer helpers as a standalone module so
// the testcontainers-go/modules/minio dependency stays out of every other module's
// graph (only adapters/s3 — the rightful minio user — and this module declare it).
// The helpers themselves live in minioctr.go behind the `integration` build tag.
package minioctr
