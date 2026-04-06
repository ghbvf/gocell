package s3

import (
	"context"
	"encoding/xml"
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

// s3ErrorResponse is the XML error response format expected by the AWS SDK.
type s3ErrorResponse struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeS3Error(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(statusCode)
	resp := s3ErrorResponse{Code: code, Message: message}
	data, _ := xml.Marshal(resp)
	_, _ = w.Write(data)
}

func testS3Server(t *testing.T) *httptest.Server {
	t.Helper()
	skipIfNoListener(t)

	objects := make(map[string][]byte)

	mux := http.NewServeMux()

	// Handle all requests via a catch-all pattern.
	// AWS SDK sends requests in path-style: /bucket/key
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Extract key from path: /bucket/key or /bucket (for HEAD bucket)
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
				writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "bucket not found")
				return
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodPut:
			if key == "" {
				writeS3Error(w, http.StatusBadRequest, "InvalidRequest", "key required")
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeS3Error(w, http.StatusInternalServerError, "InternalError", "read error")
				return
			}
			objects[key] = body
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			if key == "" {
				writeS3Error(w, http.StatusBadRequest, "InvalidRequest", "key required")
				return
			}
			data, ok := objects[key]
			if !ok {
				writeS3Error(w, http.StatusNotFound, "NoSuchKey", "not found")
				return
			}
			w.WriteHeader(http.StatusOK)
			if _, writeErr := w.Write(data); writeErr != nil {
				t.Errorf("failed to write response: %v", writeErr)
			}

		case http.MethodDelete:
			if key == "" {
				writeS3Error(w, http.StatusBadRequest, "InvalidRequest", "key required")
				return
			}
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)

		default:
			writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed")
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
