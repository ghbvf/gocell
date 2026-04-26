// Package ports defines accesscore's outbound dependency interfaces.
//
// Cross-cell HTTP clients (e.g. ConfigClient that calls configcore's internal
// GET /internal/v1/config/{key}) are abstracted here so accesscore slices can
// be unit-tested with stub implementations. Concrete HTTP-backed
// implementations live in cells/accesscore/internal/adapters/http/.
package ports

import "context"

// ConfigEntry holds the fields returned by the internal config GET endpoint
// (contract: http.config.internal.get.v1).
//
// IMPORTANT: when Sensitive is true, Value is the redacted placeholder
// "******" — configcore's HandleGet shares the same response mapper for both
// public and internal listeners, applying redaction uniformly. Callers MUST
// NOT log or persist Value; refetch is only for triggering reload of cached
// metadata, not for reading sensitive plaintext.
type ConfigEntry struct {
	Key       string
	Value     string
	Sensitive bool
	Version   int
}

// ConfigClient abstracts the cross-cell HTTP call to configcore's internal
// GET /internal/v1/config/{key} endpoint (contract: http.config.internal.get.v1).
// Implementations live in cells/accesscore/internal/adapters/http/.
type ConfigClient interface {
	GetEntry(ctx context.Context, key string) (ConfigEntry, error)
}
