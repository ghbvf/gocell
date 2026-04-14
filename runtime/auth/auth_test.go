package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClaims_ContextRoundTrip(t *testing.T) {
	claims := Claims{
		Subject:   "user-123",
		Issuer:    "gocell",
		Audience:  []string{"api"},
		ExpiresAt: time.Now().Add(time.Hour),
		IssuedAt:  time.Now(),
		Roles:     []string{"admin", "user"},
		Extra:     map[string]any{"tenant": "acme"},
	}

	ctx := WithClaims(context.Background(), claims)
	got, ok := ClaimsFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "user-123", got.Subject)
	assert.Equal(t, "gocell", got.Issuer)
	assert.Equal(t, []string{"admin", "user"}, got.Roles)
	assert.Equal(t, "acme", got.Extra["tenant"])
}

func TestClaims_MissingFromContext(t *testing.T) {
	_, ok := ClaimsFrom(context.Background())
	assert.False(t, ok)
}
