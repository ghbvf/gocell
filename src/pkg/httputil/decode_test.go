package httputil

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		dst        func() any // factory to create fresh decode target
		wantCode   errcode.Code
		wantReason string // expected details["reason"]
	}{
		{
			name:     "valid struct",
			body:     `{"name":"test"}`,
			dst:      func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode: "", // no error
		},
		{
			name:     "valid map",
			body:     `{"foo":"bar","extra":"ok"}`,
			dst:      func() any { return &map[string]json.RawMessage{} },
			wantCode: "", // maps accept any field
		},
		{
			name:       "empty body",
			body:       "",
			dst:        func() any { return &struct{}{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "empty body",
		},
		{
			name:       "malformed JSON",
			body:       `{invalid`,
			dst:        func() any { return &struct{}{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "malformed JSON",
		},
		{
			name:       "type mismatch",
			body:       `{"count":"notanumber"}`,
			dst:        func() any { return &struct{ Count int `json:"count"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "type mismatch",
		},
		{
			name:     "unknown fields accepted",
			body:     `{"name":"test","unknown":"value"}`,
			dst:      func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode: "", // backward compatible: unknown fields are silently ignored
		},
		{
			name:       "truncated JSON",
			body:       `{"username":`,
			dst:        func() any { return &struct{ Username string `json:"username"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "malformed JSON",
		},
		{
			name:       "trailing content",
			body:       `{"name":"test"}garbage`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
		{
			name:       "multiple JSON objects",
			body:       `{"name":"test"}{"role":"admin"}`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
		{
			name:       "trailing close brace",
			body:       `{"name":"ok"}}`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
		{
			name:       "trailing close bracket",
			body:       `{"name":"ok"}]`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
		{
			name:       "trailing close bracket with space",
			body:       `{"name":"ok"} ]`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")

			dst := tt.dst()
			err := DecodeJSON(r, dst)

			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr), "error should be *errcode.Error")
			assert.Equal(t, tt.wantCode, ecErr.Code)
			if tt.wantReason != "" {
				assert.Equal(t, tt.wantReason, ecErr.Details["reason"])
			}
		})
	}
}

func TestDecodeJSON_MaxBytesExceeded(t *testing.T) {
	// Create a request with a large body
	bigBody := `{"data":"` + strings.Repeat("x", 1024) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(bigBody))
	r.Header.Set("Content-Type", "application/json")
	// Wrap body with MaxBytesReader to simulate a size limit
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 10)

	var dst struct {
		Data string `json:"data"`
	}
	err := DecodeJSON(r, &dst)

	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error should be *errcode.Error")
	assert.Equal(t, errcode.ErrBodyTooLarge, ecErr.Code)
}
