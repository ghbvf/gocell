package s3

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipIfNoListener(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: cannot listen on TCP (sandbox?): %v", err)
	}
	ln.Close()
}

func testS3Server(t *testing.T) *httptest.Server {
	t.Helper()
	skipIfNoListener(t)

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		bucket := ""
		key := ""
		if len(parts) >= 1 {
			bucket = parts[0]
		}
		if len(parts) >= 2 {
			key = parts[1]
		}

		switch r.Method {
		case http.MethodHead:
			if bucket == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodPut:
			if key == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, err := io.ReadAll(r.Body); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	return httptest.NewServer(mux)
}

func testConfig(serverURL string) Config {
	return Config{
		Endpoint:       serverURL,
		Region:         "us-east-1",
		Bucket:         "test-bucket",
		AccessKeyID:    "test-key",
		SecretAccessKey: "test-secret",
		UsePathStyle:   true,
		HTTPTimeout:    5 * time.Second,
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name:    "valid config",
			config:  testConfig("http://localhost:9000"),
			wantErr: false,
		},
		{
			name:    "missing endpoint",
			config:  Config{Region: "r", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
			wantErr: true,
		},
		{
			name:    "missing region",
			config:  Config{Endpoint: "e", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
			wantErr: true,
		},
		{
			name:    "missing bucket",
			config:  Config{Endpoint: "e", Region: "r", AccessKeyID: "k", SecretAccessKey: "s"},
			wantErr: true,
		},
		{
			name:    "missing access key",
			config:  Config{Endpoint: "e", Region: "r", Bucket: "b", SecretAccessKey: "s"},
			wantErr: true,
		},
		{
			name:    "missing secret key",
			config:  Config{Endpoint: "e", Region: "r", Bucket: "b", AccessKeyID: "k"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
				var ec *errcode.Error
				require.ErrorAs(t, err, &ec)
				assert.Equal(t, ErrAdapterS3Config, ec.Code)
			} else {
				require.NoError(t, err)
			}
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

func TestConfigFromEnv_Fallback(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "http://legacy:9000")
	t.Setenv("S3_REGION", "us-west-2")
	t.Setenv("S3_BUCKET", "legacy-bucket")
	t.Setenv("S3_ACCESS_KEY_ID", "legacykey")
	t.Setenv("S3_SECRET_ACCESS_KEY", "legacysecret")

	cfg := ConfigFromEnv()
	assert.Equal(t, "http://legacy:9000", cfg.Endpoint)
	assert.Equal(t, "us-west-2", cfg.Region)
	assert.Equal(t, "legacy-bucket", cfg.Bucket)
	assert.Equal(t, "legacykey", cfg.AccessKeyID)
	assert.Equal(t, "legacysecret", cfg.SecretAccessKey)
}

func TestClient_Health(t *testing.T) {
	server := testS3Server(t)
	defer server.Close()

	client, err := New(testConfig(server.URL))
	require.NoError(t, err)

	err = client.Health(context.Background())
	require.NoError(t, err)
}

func TestClient_Upload(t *testing.T) {
	server := testS3Server(t)
	defer server.Close()

	client, err := New(testConfig(server.URL))
	require.NoError(t, err)

	err = client.Upload(context.Background(), "test/file.txt", []byte("hello world"), "text/plain")
	require.NoError(t, err)
}

func TestClient_Upload_DefaultContentType(t *testing.T) {
	server := testS3Server(t)
	defer server.Close()

	client, err := New(testConfig(server.URL))
	require.NoError(t, err)

	err = client.Upload(context.Background(), "test/bin", []byte{0x00}, "")
	require.NoError(t, err)
}
