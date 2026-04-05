package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testS3Server(t *testing.T) *httptest.Server {
	t.Helper()

	objects := make(map[string][]byte)

	mux := http.NewServeMux()

	// Handle all requests via a catch-all pattern.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Extract key from path: /bucket/key
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
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodPut:
			if key == "" {
				http.Error(w, "key required", http.StatusBadRequest)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read error", http.StatusInternalServerError)
				return
			}
			objects[key] = body
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			if key == "" {
				http.Error(w, "key required", http.StatusBadRequest)
				return
			}
			data, ok := objects[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			if _, writeErr := w.Write(data); writeErr != nil {
				t.Errorf("failed to write response: %v", writeErr)
			}

		case http.MethodDelete:
			if key == "" {
				http.Error(w, "key required", http.StatusBadRequest)
				return
			}
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_REGION", "eu-west-1")
	t.Setenv("S3_BUCKET", "my-bucket")
	t.Setenv("S3_ACCESS_KEY_ID", "key123")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret456")
	t.Setenv("S3_USE_PATH_STYLE", "true")

	cfg := ConfigFromEnv()
	assert.Equal(t, "http://localhost:9000", cfg.Endpoint)
	assert.Equal(t, "eu-west-1", cfg.Region)
	assert.Equal(t, "my-bucket", cfg.Bucket)
	assert.Equal(t, "key123", cfg.AccessKeyID)
	assert.Equal(t, "secret456", cfg.SecretAccessKey)
	assert.True(t, cfg.UsePathStyle)
}

func TestClient_Health(t *testing.T) {
	server := testS3Server(t)
	defer server.Close()

	client, err := New(testConfig(server.URL))
	require.NoError(t, err)

	err = client.Health(context.Background())
	require.NoError(t, err)
}

func TestClient_UploadDownloadDelete(t *testing.T) {
	server := testS3Server(t)
	defer server.Close()

	client, err := New(testConfig(server.URL))
	require.NoError(t, err)

	ctx := context.Background()

	// Upload.
	data := []byte("hello world")
	err = client.Upload(ctx, "test/file.txt", data, "text/plain")
	require.NoError(t, err)

	// Download.
	downloaded, err := client.Download(ctx, "test/file.txt")
	require.NoError(t, err)
	assert.Equal(t, data, downloaded)

	// Delete.
	err = client.Delete(ctx, "test/file.txt")
	require.NoError(t, err)

	// Download after delete should fail.
	_, err = client.Download(ctx, "test/file.txt")
	require.Error(t, err)
}

func TestClient_Upload_DefaultContentType(t *testing.T) {
	server := testS3Server(t)
	defer server.Close()

	client, err := New(testConfig(server.URL))
	require.NoError(t, err)

	err = client.Upload(context.Background(), "test/bin", []byte{0x00}, "")
	require.NoError(t, err)
}

func TestClient_Delete_NotFound(t *testing.T) {
	server := testS3Server(t)
	defer server.Close()

	client, err := New(testConfig(server.URL))
	require.NoError(t, err)

	// Deleting a non-existent key should succeed (idempotent).
	err = client.Delete(context.Background(), "nonexistent")
	require.NoError(t, err)
}

func TestClient_PresignedGet(t *testing.T) {
	cfg := testConfig("http://localhost:9000")
	client, err := New(cfg)
	require.NoError(t, err)

	presignedURL, err := client.PresignedGet("path/to/file.txt", 15*time.Minute)
	require.NoError(t, err)
	assert.Contains(t, presignedURL, "X-Amz-Signature=")
	assert.Contains(t, presignedURL, "X-Amz-Expires=900")
	assert.Contains(t, presignedURL, "path/to/file.txt")
}

func TestClient_PresignedPut(t *testing.T) {
	cfg := testConfig("http://localhost:9000")
	client, err := New(cfg)
	require.NoError(t, err)

	presignedURL, err := client.PresignedPut("uploads/data.bin", 1*time.Hour)
	require.NoError(t, err)
	assert.Contains(t, presignedURL, "X-Amz-Signature=")
	assert.Contains(t, presignedURL, "X-Amz-Expires=3600")
}

func TestClient_PresignedGet_InvalidTTL(t *testing.T) {
	cfg := testConfig("http://localhost:9000")
	client, err := New(cfg)
	require.NoError(t, err)

	_, err = client.PresignedGet("key", -1*time.Second)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterS3Presign, ec.Code)
}

func TestClient_PresignedGet_TTLTooLong(t *testing.T) {
	cfg := testConfig("http://localhost:9000")
	client, err := New(cfg)
	require.NoError(t, err)

	_, err = client.PresignedGet("key", 8*24*time.Hour)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterS3Presign, ec.Code)
}

func TestDeriveSigningKey(t *testing.T) {
	// Deterministic test: same inputs → same output.
	key1 := deriveSigningKey("secret", "20260405", "us-east-1", "s3")
	key2 := deriveSigningKey("secret", "20260405", "us-east-1", "s3")
	assert.Equal(t, key1, key2)

	// Different date → different key.
	key3 := deriveSigningKey("secret", "20260406", "us-east-1", "s3")
	assert.NotEqual(t, key1, key3)
}
