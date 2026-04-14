package s3

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	cfg := Config{
		Endpoint: "http://localhost:9000", Region: "us-east-1",
		Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
		// HTTPTimeout is 0 — should use default.
	}
	client, err := New(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNew_InvalidConfig(t *testing.T) {
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestConfigFromEnv_UsePathStyle_False(t *testing.T) {
	t.Setenv("GOCELL_S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("GOCELL_S3_REGION", "us-east-1")
	t.Setenv("GOCELL_S3_BUCKET", "b")
	t.Setenv("GOCELL_S3_ACCESS_KEY", "k")
	t.Setenv("GOCELL_S3_SECRET_KEY", "s")
	// GOCELL_S3_USE_PATH_STYLE not set — should be false.

	cfg := ConfigFromEnv()
	assert.False(t, cfg.UsePathStyle)
}
