package s3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConfig(endpoint string) Config {
	return Config{
		Endpoint:  endpoint,
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Bucket:    "test-bucket",
		UseSSL:   false,
		Region:    "us-east-1",
	}
}

// newTestClient creates a Client pointing at the given httptest server.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	// Strip scheme from server URL for the endpoint.
	endpoint := strings.TrimPrefix(server.URL, "http://")
	cfg := validConfig(endpoint)
	client, err := New(cfg, WithHTTPClient(server.Client()))
	require.Nil(t, err)
	return client
}

// --- Config Tests ---

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		missing []string
	}{
		{
			name:    "valid config",
			cfg:     validConfig("localhost:9000"),
			wantErr: false,
		},
		{
			name: "missing endpoint",
			cfg: Config{
				AccessKey: "ak",
				SecretKey: "sk",
				Bucket:    "b",
			},
			wantErr: true,
			missing: []string{"Endpoint"},
		},
		{
			name: "missing multiple fields",
			cfg: Config{
				Endpoint: "localhost:9000",
			},
			wantErr: true,
			missing: []string{"AccessKey", "SecretKey", "Bucket"},
		},
		{
			name:    "all fields missing",
			cfg:     Config{},
			wantErr: true,
			missing: []string{"Endpoint", "AccessKey", "SecretKey", "Bucket"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.NotNil(t, err)
				assert.Equal(t, ErrS3Config, err.Code)
				if tt.missing != nil {
					assert.Equal(t, tt.missing, err.Details["missing"])
				}
			} else {
				assert.Nil(t, err)
			}
		})
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("GOCELL_S3_ENDPOINT", "minio.local:9000")
	t.Setenv("GOCELL_S3_ACCESS_KEY", "mykey")
	t.Setenv("GOCELL_S3_SECRET_KEY", "mysecret")
	t.Setenv("GOCELL_S3_BUCKET", "my-bucket")
	t.Setenv("GOCELL_S3_USE_SSL", "true")
	t.Setenv("GOCELL_S3_REGION", "eu-west-1")

	cfg := ConfigFromEnv()
	assert.Equal(t, "minio.local:9000", cfg.Endpoint)
	assert.Equal(t, "mykey", cfg.AccessKey)
	assert.Equal(t, "mysecret", cfg.SecretKey)
	assert.Equal(t, "my-bucket", cfg.Bucket)
	assert.True(t, cfg.UseSSL)
	assert.Equal(t, "eu-west-1", cfg.Region)
}

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("GOCELL_S3_ENDPOINT", "")
	t.Setenv("GOCELL_S3_ACCESS_KEY", "")
	t.Setenv("GOCELL_S3_SECRET_KEY", "")
	t.Setenv("GOCELL_S3_BUCKET", "")
	t.Setenv("GOCELL_S3_USE_SSL", "")
	t.Setenv("GOCELL_S3_REGION", "")

	cfg := ConfigFromEnv()
	assert.False(t, cfg.UseSSL)
	assert.Equal(t, "us-east-1", cfg.Region)
}

// --- New Tests ---

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "valid config creates client",
			cfg:     validConfig("localhost:9000"),
			wantErr: false,
		},
		{
			name:    "invalid config returns error",
			cfg:     Config{},
			wantErr: true,
		},
		{
			name: "ssl config uses https base URL",
			cfg: Config{
				Endpoint:  "s3.amazonaws.com",
				AccessKey: "ak",
				SecretKey: "sk",
				Bucket:    "b",
				UseSSL:   true,
				Region:    "us-east-1",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(tt.cfg)
			if tt.wantErr {
				assert.NotNil(t, err)
				assert.Nil(t, client)
			} else {
				assert.Nil(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestNewWithSSL(t *testing.T) {
	cfg := Config{
		Endpoint:  "s3.amazonaws.com",
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    "b",
		UseSSL:   true,
		Region:    "us-east-1",
	}
	client, err := New(cfg)
	require.Nil(t, err)
	assert.True(t, strings.HasPrefix(client.baseURL, "https://"))
}

func TestNewDefaultRegion(t *testing.T) {
	cfg := Config{
		Endpoint:  "localhost:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    "b",
	}
	client, err := New(cfg)
	require.Nil(t, err)
	assert.Equal(t, "us-east-1", client.cfg.Region)
}

// --- Health Tests ---

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "healthy endpoint",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "unhealthy endpoint returns error",
			statusCode: http.StatusForbidden,
			wantErr:    true,
		},
		{
			name:       "not found bucket",
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodHead, r.Method)
				assert.True(t, strings.HasPrefix(r.URL.Path, "/test-bucket"))
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := newTestClient(t, server)
			err := client.Health(context.Background())
			if tt.wantErr {
				assert.NotNil(t, err)
				assert.Equal(t, ErrS3Health, err.Code)
			} else {
				assert.Nil(t, err)
			}
		})
	}
}

func TestHealthConnectionRefused(t *testing.T) {
	cfg := validConfig("localhost:1") // unlikely to be listening
	client, cerr := New(cfg)
	require.Nil(t, cerr)

	// Override with a short timeout to avoid long waits.
	client.httpClient = &http.Client{Timeout: 100 * time.Millisecond}

	err := client.Health(context.Background())
	assert.NotNil(t, err)
	assert.Equal(t, ErrS3Health, err.Code)
}

// --- Upload Tests ---

func TestUpload(t *testing.T) {
	tests := []struct {
		name       string
		input      UploadInput
		statusCode int
		wantErr    bool
	}{
		{
			name: "successful upload",
			input: UploadInput{
				Key:         "docs/readme.txt",
				Body:        strings.NewReader("hello world"),
				ContentType: "text/plain",
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "upload with default content type",
			input: UploadInput{
				Key:  "data/binary.bin",
				Body: bytes.NewReader([]byte{0x00, 0x01, 0x02}),
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "upload server error",
			input: UploadInput{
				Key:  "fail/object.txt",
				Body: strings.NewReader("data"),
			},
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Contains(t, r.URL.Path, tt.input.Key)

				// Verify authorization header is present.
				assert.NotEmpty(t, r.Header.Get("Authorization"))
				assert.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256")

				// Read body.
				body, readErr := io.ReadAll(r.Body)
				assert.NoError(t, readErr)
				assert.NotEmpty(t, body)

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := newTestClient(t, server)
			err := client.Upload(context.Background(), tt.input)
			if tt.wantErr {
				assert.NotNil(t, err)
				assert.Equal(t, ErrS3Upload, err.Code)
			} else {
				assert.Nil(t, err)
			}
		})
	}
}

func TestUploadDefaultContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	err := client.Upload(context.Background(), UploadInput{
		Key:  "test.bin",
		Body: strings.NewReader("data"),
	})
	assert.Nil(t, err)
}

// --- Download Tests ---

type downloadErrKind int

const (
	downloadErrNotFound downloadErrKind = iota
	downloadErrGeneric
)

func TestDownload(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		statusCode  int
		body        string
		contentType string
		wantErr     bool
		wantErrKind downloadErrKind
	}{
		{
			name:        "successful download",
			key:         "docs/readme.txt",
			statusCode:  http.StatusOK,
			body:        "hello world",
			contentType: "text/plain",
			wantErr:     false,
		},
		{
			name:        "not found",
			key:         "missing/object.txt",
			statusCode:  http.StatusNotFound,
			body:        "<Error><Code>NoSuchKey</Code></Error>",
			contentType: "application/xml",
			wantErr:     true,
			wantErrKind: downloadErrNotFound,
		},
		{
			name:        "server error",
			key:         "error/object.txt",
			statusCode:  http.StatusInternalServerError,
			body:        "internal error",
			contentType: "text/plain",
			wantErr:     true,
			wantErrKind: downloadErrGeneric,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Contains(t, r.URL.Path, tt.key)

				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.statusCode)
				_, wErr := w.Write([]byte(tt.body))
				assert.NoError(t, wErr)
			}))
			defer server.Close()

			client := newTestClient(t, server)
			output, err := client.Download(context.Background(), tt.key)
			if tt.wantErr {
				assert.NotNil(t, err)
				assert.Nil(t, output)
				switch tt.wantErrKind {
				case downloadErrNotFound:
					assert.Equal(t, ErrS3NotFound, err.Code)
				case downloadErrGeneric:
					assert.Equal(t, ErrS3Download, err.Code)
				}
			} else {
				assert.Nil(t, err)
				require.NotNil(t, output)
				defer func() {
					cErr := output.Body.Close()
					assert.NoError(t, cErr)
				}()

				data, readErr := io.ReadAll(output.Body)
				assert.NoError(t, readErr)
				assert.Equal(t, tt.body, string(data))
				assert.Equal(t, tt.contentType, output.ContentType)
			}
		})
	}
}

// --- Delete Tests ---

func TestDelete(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "successful delete",
			key:        "docs/readme.txt",
			statusCode: http.StatusNoContent,
			wantErr:    false,
		},
		{
			name:       "delete non-existent key (idempotent)",
			key:        "missing/key.txt",
			statusCode: http.StatusNoContent,
			wantErr:    false,
		},
		{
			name:       "delete server error",
			key:        "error/key.txt",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Contains(t, r.URL.Path, tt.key)
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := newTestClient(t, server)
			err := client.Delete(context.Background(), tt.key)
			if tt.wantErr {
				assert.NotNil(t, err)
				assert.Equal(t, ErrS3Delete, err.Code)
			} else {
				assert.Nil(t, err)
			}
		})
	}
}

// --- Presigned URL Tests ---

func TestPresignedGetURL(t *testing.T) {
	cfg := validConfig("localhost:9000")
	client, err := New(cfg)
	require.Nil(t, err)

	presignedURL, perr := client.PresignedGetURL("docs/readme.txt", 5*time.Minute)
	assert.Nil(t, perr)
	assert.NotEmpty(t, presignedURL)

	parsed, parseErr := neturl.Parse(presignedURL)
	assert.NoError(t, parseErr)
	assert.Contains(t, parsed.Path, "test-bucket/docs/readme.txt")
	assert.Equal(t, "AWS4-HMAC-SHA256", parsed.Query().Get("X-Amz-Algorithm"))
	assert.Equal(t, "300", parsed.Query().Get("X-Amz-Expires"))
	assert.NotEmpty(t, parsed.Query().Get("X-Amz-Signature"))
	assert.NotEmpty(t, parsed.Query().Get("X-Amz-Credential"))
	assert.Equal(t, "host", parsed.Query().Get("X-Amz-SignedHeaders"))
}

func TestPresignedPutURL(t *testing.T) {
	cfg := validConfig("localhost:9000")
	client, err := New(cfg)
	require.Nil(t, err)

	presignedURL, perr := client.PresignedPutURL("uploads/file.bin", 10*time.Minute)
	assert.Nil(t, perr)
	assert.NotEmpty(t, presignedURL)

	parsed, parseErr := neturl.Parse(presignedURL)
	assert.NoError(t, parseErr)
	assert.Contains(t, parsed.Path, "test-bucket/uploads/file.bin")
	assert.Equal(t, "600", parsed.Query().Get("X-Amz-Expires"))
}

func TestPresignedURLDefaultTTL(t *testing.T) {
	cfg := validConfig("localhost:9000")
	client, err := New(cfg)
	require.Nil(t, err)

	presignedURL, perr := client.PresignedGetURL("key", 0)
	assert.Nil(t, perr)

	parsed, parseErr := neturl.Parse(presignedURL)
	assert.NoError(t, parseErr)
	assert.Equal(t, "900", parsed.Query().Get("X-Amz-Expires")) // 15 min default
}

func TestPresignedURLExceedsMaxTTL(t *testing.T) {
	cfg := validConfig("localhost:9000")
	client, err := New(cfg)
	require.Nil(t, err)

	_, perr := client.PresignedGetURL("key", 8*24*time.Hour)
	assert.NotNil(t, perr)
	assert.Equal(t, ErrS3Presign, perr.Code)
}

// --- Signature Tests ---

func TestSignV4AuthorizationHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256"))
		assert.Contains(t, auth, "Credential=AKIAIOSFODNN7EXAMPLE/")
		assert.Contains(t, auth, "SignedHeaders=")
		assert.Contains(t, auth, "Signature=")

		assert.NotEmpty(t, r.Header.Get("x-amz-date"))
		assert.NotEmpty(t, r.Header.Get("x-amz-content-sha256"))

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	err := client.Health(context.Background())
	assert.Nil(t, err)
}

func TestDeriveSigningKey(t *testing.T) {
	// Deterministic: same inputs produce same key.
	key1 := deriveSigningKey("secret", "20260405", "us-east-1", "s3")
	key2 := deriveSigningKey("secret", "20260405", "us-east-1", "s3")
	assert.Equal(t, key1, key2)

	// Different date produces different key.
	key3 := deriveSigningKey("secret", "20260406", "us-east-1", "s3")
	assert.NotEqual(t, key1, key3)

	// Different region produces different key.
	key4 := deriveSigningKey("secret", "20260405", "eu-west-1", "s3")
	assert.NotEqual(t, key1, key4)
}

func TestSha256Hex(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "empty payload",
			data: nil,
			want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "hello",
			data: []byte("hello"),
			want: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sha256Hex(tt.data))
		})
	}
}

// --- Integration-style Tests (using httptest) ---

func TestUploadThenDownloadThenDelete(t *testing.T) {
	store := make(map[string][]byte)
	storeContentType := make(map[string]string)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path

		switch r.Method {
		case http.MethodPut:
			body, readErr := io.ReadAll(r.Body)
			assert.NoError(t, readErr)
			store[key] = body
			storeContentType[key] = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			body, ok := store[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", storeContentType[key])
			w.WriteHeader(http.StatusOK)
			_, wErr := w.Write(body)
			assert.NoError(t, wErr)

		case http.MethodDelete:
			delete(store, key)
			delete(storeContentType, key)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)

	// Upload
	content := "test file content"
	uploadErr := client.Upload(context.Background(), UploadInput{
		Key:         "integration/test.txt",
		Body:        strings.NewReader(content),
		ContentType: "text/plain",
	})
	require.Nil(t, uploadErr)

	// Download
	output, dlErr := client.Download(context.Background(), "integration/test.txt")
	require.Nil(t, dlErr)
	require.NotNil(t, output)

	data, readErr := io.ReadAll(output.Body)
	assert.NoError(t, readErr)
	cErr := output.Body.Close()
	assert.NoError(t, cErr)
	assert.Equal(t, content, string(data))
	assert.Equal(t, "text/plain", output.ContentType)

	// Delete
	delErr := client.Delete(context.Background(), "integration/test.txt")
	assert.Nil(t, delErr)

	// Download after delete should 404
	_, dlErr2 := client.Download(context.Background(), "integration/test.txt")
	assert.NotNil(t, dlErr2)
	assert.Equal(t, ErrS3NotFound, dlErr2.Code)
}

func TestWithHTTPClientOption(t *testing.T) {
	customClient := &http.Client{Timeout: 5 * time.Second}
	cfg := validConfig("localhost:9000")
	client, err := New(cfg, WithHTTPClient(customClient))
	require.Nil(t, err)
	assert.Equal(t, customClient, client.httpClient)
}
