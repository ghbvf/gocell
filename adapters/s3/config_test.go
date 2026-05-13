package s3

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/clock"
)

func TestEnvWithFallback_LegacyVar(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "http://legacy:9000")
	// Primary var not set, legacy should be returned.
	v := envWithFallback("GOCELL_S3_ENDPOINT_UNSET", "S3_ENDPOINT")
	assert.Equal(t, "http://legacy:9000", v)
}

func TestEnvWithFallback_NoVars(t *testing.T) {
	v := envWithFallback("COMPLETELY_MISSING_VAR", "ALSO_MISSING_VAR")
	assert.Equal(t, "", v)
}

func TestNew_DefaultTimeout(t *testing.T) {
	// New now requires a context (sync HeadBucket probe on construction).
	// Use a mock to avoid real network I/O in this unit test.
	mock := &mockHeadBucket{errFn: func(_ int64) error { return nil }}
	cfg := Config{
		Endpoint: "http://127.0.0.1:9000", Region: "us-east-1",
		Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
		Clock: clock.Real(), // required after KERNEL-CLOCK-LEAF-FALLBACK-01
		// HTTPTimeout is 0 — should use default.
	}
	client, err := newClientWithHead(context.Background(), cfg, mock)
	assert.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNew_InvalidConfig(t *testing.T) {
	_, err := New(context.Background(), Config{})
	assert.Error(t, err)
}

func TestConfigFromEnv_UsePathStyle_False(t *testing.T) {
	t.Setenv("GOCELL_S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("GOCELL_S3_REGION", "us-east-1")
	t.Setenv("GOCELL_S3_BUCKET", "b")
	t.Setenv("GOCELL_S3_ACCESS_KEY", "k")
	t.Setenv("GOCELL_S3_SECRET_KEY", "s")
	// GOCELL_S3_USE_PATH_STYLE not set — should be false.

	cfg := ConfigFromEnv()
	assert.False(t, cfg.UsePathStyle)
}
