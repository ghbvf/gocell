package s3

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	valid := Config{
		Endpoint: "http://localhost:9000", Region: "us-east-1",
		Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
	}
	require.NoError(t, valid.Validate())

	for _, tc := range []struct {
		name   string
		config Config
	}{
		{"missing endpoint", Config{Region: "r", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}},
		{"missing region", Config{Endpoint: "e", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}},
		{"missing bucket", Config{Endpoint: "e", Region: "r", AccessKeyID: "k", SecretAccessKey: "s"}},
		{"missing access key", Config{Endpoint: "e", Region: "r", Bucket: "b", SecretAccessKey: "s"}},
		{"missing secret key", Config{Endpoint: "e", Region: "r", Bucket: "b", AccessKeyID: "k"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, ErrAdapterS3Config, ec.Code)
		})
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("GOCELL_S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("GOCELL_S3_REGION", "eu-west-1")
	t.Setenv("GOCELL_S3_BUCKET", "my-bucket")
	t.Setenv("GOCELL_S3_ACCESS_KEY", "key123")
	t.Setenv("GOCELL_S3_SECRET_KEY", "secret456")
	t.Setenv("GOCELL_S3_USE_PATH_STYLE", "true")

	cfg := ConfigFromEnv()
	assert.Equal(t, "http://localhost:9000", cfg.Endpoint)
	assert.Equal(t, "eu-west-1", cfg.Region)
	assert.Equal(t, "my-bucket", cfg.Bucket)
	assert.Equal(t, "key123", cfg.AccessKeyID)
	assert.Equal(t, "secret456", cfg.SecretAccessKey)
	assert.True(t, cfg.UsePathStyle)
}

func TestNew_ValidConfig(t *testing.T) {
	cfg := Config{
		Endpoint: "http://localhost:9000", Region: "us-east-1",
		Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
	}
	client, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.SDK(), "SDK() must expose underlying S3 client")
}
