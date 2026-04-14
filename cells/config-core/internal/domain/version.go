package domain

import "time"

// ConfigVersion tracks a specific published snapshot of a ConfigEntry value.
type ConfigVersion struct {
	ID          string
	ConfigID    string
	Version     int
	Value       string
	PublishedAt *time.Time // nil = not published
}
