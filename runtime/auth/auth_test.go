package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrincipal_ContextRoundTrip(t *testing.T) {
	p := &Principal{
		Kind:    PrincipalUser,
		Subject: "user-123",
		Roles:   []string{"admin", "user"},
	}

	ctx := WithPrincipal(context.Background(), p)
	got, ok := FromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, "user-123", got.Subject)
	assert.Equal(t, []string{"admin", "user"}, got.Roles)
}

func TestPrincipal_MissingFromContext(t *testing.T) {
	_, ok := FromContext(context.Background())
	assert.False(t, ok)
}
