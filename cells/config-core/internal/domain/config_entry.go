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

	// KeyID is the encryption key version used to encrypt this entry's value.
	// Empty for non-sensitive entries or legacy plaintext rows.
	// Storage metadata only — never included in HTTP DTOs or event payloads.
	KeyID string

	// Stale is true when KeyID differs from the current active key version,
	// signalling that this entry should be lazily re-encrypted.
	// Storage metadata only — never included in HTTP DTOs or event payloads.
	Stale bool
}
