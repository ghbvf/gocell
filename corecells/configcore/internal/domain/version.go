package domain

import "time"

// ConfigVersion tracks a specific published snapshot of a ConfigEntry value.
type ConfigVersion struct {
	ID          string
	ConfigID    string
	Version     int
	Value       string
	Sensitive   bool       // inherits ConfigEntry.Sensitive at publish time; drives DTO redaction.
	PublishedAt *time.Time // nil = not published
}
