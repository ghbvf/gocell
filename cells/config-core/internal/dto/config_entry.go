// Package dto provides handler-level response types for config-core,
// shared across slices that return the same entity shape.
package dto

import (
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
)

// ConfigEntryResponse is the public DTO for ConfigEntry, isolating the API
// contract from the domain model.
type ConfigEntryResponse struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ToConfigEntryResponse converts a domain.ConfigEntry to its API response DTO.
func ToConfigEntryResponse(e *domain.ConfigEntry) ConfigEntryResponse {
	return ConfigEntryResponse{
		ID: e.ID, Key: e.Key, Value: e.Value, Version: e.Version,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}
