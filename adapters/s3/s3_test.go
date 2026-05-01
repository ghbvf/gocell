package s3

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
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
		// Use a loopback endpoint so TLS validation passes and the test exercises
		// the field-missing checks that follow. "e" was a bare non-loopback host
		// that now fails TLS validation first (SEC-FAIL-CLOSED, phase 2).
		{"missing region", Config{Endpoint: "http://localhost:9000", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}},
		{"missing bucket", Config{Endpoint: "http://localhost:9000", Region: "r", AccessKeyID: "k", SecretAccessKey: "s"}},
		{"missing access key", Config{Endpoint: "http://localhost:9000", Region: "r", Bucket: "b", SecretAccessKey: "s"}},
		{"missing secret key", Config{Endpoint: "http://localhost:9000", Region: "r", Bucket: "b", AccessKeyID: "k"}},
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

// TestConfigValidate_RejectsNonTLSEndpoint verifies that Config.Validate
// rejects non-TLS remote endpoints once phase-2 wires secutil.ValidateTLSEndpoint
// into the adapter. During TDD phase-1 these rejection cases will FAIL because
// the stub returns nil for all inputs (fail-open).
//
// Loopback exception: http://127.0.0.1:9000 is accepted regardless of scheme.
func TestConfigValidate_RejectsNonTLSEndpoint(t *testing.T) {
	t.Parallel()

	baseValid := Config{
		Region: "us-east-1", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
	}

	tests := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{
			name:     "http remote — reject",
			endpoint: "http://s3.prod:9000",
			wantErr:  true,
		},
		{
			name:     "https remote — ok",
			endpoint: "https://s3.prod:9000",
			wantErr:  false,
		},
		{
			name:     "http loopback — ok",
			endpoint: "http://127.0.0.1:9000",
			wantErr:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseValid
			cfg.Endpoint = tc.endpoint
			err := cfg.Validate()
			if tc.wantErr {
				require.Error(t, err, "Validate(%q): expected TLS validation error", tc.endpoint)
				var ec *errcode.Error
				require.ErrorAs(t, err, &ec, "error must be an *errcode.Error")
				assert.Equal(t, errcode.ErrAdapterEndpointNotTLS, ec.Code)
			} else {
				require.NoError(t, err, "Validate(%q): expected no error", tc.endpoint)
			}
		})
	}
}
