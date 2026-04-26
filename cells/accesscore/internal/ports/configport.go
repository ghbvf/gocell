package ports

import "context"

// ConfigEntry holds the metadata fields returned by the internal config GET endpoint.
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
