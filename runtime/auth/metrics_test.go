package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
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
		name   string
		errMsg string
		want   string
	}{
		{"nil error", "", "ok"},
		{"expired", "token is expired", "expired"},
		{"not valid yet", "token is not valid yet", "expired"},
		{"kid", "missing kid header", "invalid_kid"},
		{"signing method", "unexpected signing method", "wrong_alg"},
		{"other", "something else", "invalid_signature"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.errMsg != "" {
				err = fmt.Errorf("%s", tt.errMsg)
			}
			assert.Equal(t, tt.want, classifyTokenError(err))
		})
	}
}
