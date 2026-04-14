// Package dto provides handler-level response types for config-core,
// shared across slices that return the same entity shape.
package dto

import (
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
)

// RedactedValue is the placeholder shown in API responses for sensitive config values.
const RedactedValue = "******"

// ConfigEntryResponse is the public DTO for ConfigEntry, isolating the API
// contract from the domain model. Sensitive values are redacted.
type ConfigEntryResponse struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Sensitive bool      `json:"sensitive"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ToConfigEntryResponse converts a domain.ConfigEntry to its API response DTO.
// Sensitive values are replaced with RedactedValue.
func ToConfigEntryResponse(e *domain.ConfigEntry) ConfigEntryResponse {
	value := e.Value
	if e.Sensitive {
		value = RedactedValue
	}
	return ConfigEntryResponse{
		ID: e.ID, Key: e.Key, Value: value, Sensitive: e.Sensitive,
		Version: e.Version, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}
