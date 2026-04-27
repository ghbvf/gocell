// Package testutil provides shared test utilities for integration tests.
package testutil

// Testcontainer images pinned to specific patch versions and immutable digests.
// Floating minor tags (e.g., "postgres:15-alpine") and tag-only patch pins are
// forbidden because they cause non-reproducible CI builds when upstream retags.
//
// Update procedure: bump the patch version, refresh the manifest-list digest
// for the exact tag, run `go test ./tests/testutil/...` to verify the format,
// then run integration tests.
const (
	PostgresImage      = "postgres:15.13-alpine@sha256:1414298ea93186123a6dcf872f778ba3bd2347edcbd2f31aa7bb2d9814ff5393"
	RedisImage         = "redis:7.4.2-alpine@sha256:02419de7eddf55aa5bcf49efb74e88fa8d931b4d77c07eff8a6b2144472b6952"
	RabbitMQImage      = "rabbitmq:3.12.14-management-alpine@sha256:0b44fbcc3a4bf22d00090f1353127577dbe1fcb109c41669733a9d7ecf6c3a78"
	VaultImage         = "hashicorp/vault:1.17@sha256:74a4ab138ab5d64725e89cd9a9c73f7040c7fe49e98b71697b275ca9a69919df"
	OTelCollectorImage = "otel/opentelemetry-collector:0.123.0@sha256:e5e4a13f0ea98e7ca1d1d809be040180540146888d0d764abd4e1277cba87350"
)
