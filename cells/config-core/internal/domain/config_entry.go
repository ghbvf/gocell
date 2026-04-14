// Package domain contains the config-core Cell domain models.
package domain

import "time"

// ConfigEntry is a versioned key-value configuration record.
type ConfigEntry struct {
	ID        string
	Key       string
	Value     string
	Sensitive bool // marks value as containing secrets (API keys, passwords, etc.)
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}
