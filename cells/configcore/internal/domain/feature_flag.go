package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"time"
)

// FlagType distinguishes boolean from percentage-based feature flags.
type FlagType string

const (
	// FlagBoolean is a simple on/off toggle.
	FlagBoolean FlagType = "boolean"
	// FlagPercentage gates access based on a deterministic hash of the subject.
	FlagPercentage FlagType = "percentage"
)

// FeatureFlag controls feature rollout via boolean toggle or percentage-based
// gradual release.
type FeatureFlag struct {
	ID                string
	Key               string
	Type              FlagType
	Enabled           bool
	RolloutPercentage int // 0-100, only meaningful when Type == FlagPercentage
	Description       string
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Evaluate determines whether the feature is enabled for the given subject.
//
// For FlagBoolean: returns f.Enabled directly.
// For FlagPercentage: returns true if f.Enabled AND hash(subject+key) % 100
// falls below f.RolloutPercentage, providing deterministic sticky assignment.
func (f *FeatureFlag) Evaluate(subject string) bool {
	if f.Type == FlagBoolean {
		return f.Enabled
	}

	// FlagPercentage
	if !f.Enabled {
		return false
	}

	h := sha256.Sum256([]byte(subject + f.Key))
	bucket := int(binary.BigEndian.Uint32(h[:4]) % 100)
	return bucket < f.RolloutPercentage
}
