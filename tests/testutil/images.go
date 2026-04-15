// Package testutil provides shared test utilities for integration tests.
package testutil

// Testcontainer images pinned to specific patch versions.
// Floating minor tags (e.g., "postgres:15-alpine") are forbidden — they cause
// non-reproducible CI builds when upstream pushes a new patch release.
//
// Update procedure: bump the patch version, run `go test ./tests/testutil/...`
// to verify the format, then run integration tests.
const (
	PostgresImage = "postgres:15.13-alpine"
	RedisImage    = "redis:7.4.2-alpine"
	RabbitMQImage = "rabbitmq:3.12.14-management-alpine"
)
