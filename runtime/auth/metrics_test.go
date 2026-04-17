package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAuthMetrics_NopProvider(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	require.NotNil(t, am)
}

func TestNewAuthMetrics_NilProvider(t *testing.T) {
	_, err := NewAuthMetrics(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
}

func TestAuthMetrics_RecordTokenVerify_NoPanic(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	// Should not panic with valid labels.
	am.recordTokenVerify("success", "ok", 5*time.Millisecond)
	am.recordTokenVerify("failure", "expired", time.Millisecond)
}

func TestAuthMetrics_RecordServiceVerify_NoPanic(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)
	am.recordServiceVerify("success", "ok")
	am.recordServiceVerify("failure", "expired")
}

func TestAuthMetrics_NilSafe(t *testing.T) {
	// nil AuthMetrics should not panic.
	var am *AuthMetrics
	am.recordTokenVerify("success", "ok", time.Millisecond)
	am.recordServiceVerify("success", "ok")
}

func TestClassifyTokenError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "ok"},
		{"jwt expired", jwt.ErrTokenExpired, "expired"},
		{"jwt not valid yet", jwt.ErrTokenNotValidYet, "expired"},
		{"jwt invalid signature", jwt.ErrTokenSignatureInvalid, "invalid_signature"},
		{"kid error", fmt.Errorf("token verification failed: missing kid header"), "invalid_kid"},
		{"wrong alg", fmt.Errorf("token verification failed: unexpected signing method: HS256"), "wrong_alg"},
		{"other", fmt.Errorf("something unexpected"), "invalid_token"},
		{"invalid_intent", errcode.New(errcode.ErrAuthInvalidTokenIntent, "x"), "invalid_intent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyTokenError(tt.err))
		})
	}
}
