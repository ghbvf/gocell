// Package dto provides handler-level response types for access-core,
// shared across slices that return the same entity shape.
package dto

import "time"

// TokenPairResponse is the public DTO for token pairs, isolating the API
// contract from the service-layer model. Shared by sessionlogin and
// sessionrefresh slices (same cell, multi-slice → internal/dto/ per DTO scope rule).
type TokenPairResponse struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}
