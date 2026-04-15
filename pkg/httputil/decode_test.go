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

// TestDecodeJSON_GoStdlibUnknownFieldFormat is a guard test that verifies
// the error message format produced by json.Decoder.DisallowUnknownFields().
// Go's encoding/json has no typed error for unknown fields (verified up to
// Go 1.25); classifyDecodeError relies on string prefix matching. If Go
// changes the format, this test fails immediately rather than silently
// misclassifying unknown-field errors as 500 Internal Server Error.
func TestDecodeJSON_GoStdlibUnknownFieldFormat(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`{"bogus": 1}`))
	dec.DisallowUnknownFields()
	var dst struct{ Name string }
	err := dec.Decode(&dst)
	require.Error(t, err)

	// Verify the prefix matches what classifyDecodeError uses (shared const).
	after, ok := strings.CutPrefix(err.Error(), unknownFieldPrefix)
	require.True(t, ok,
		"Go stdlib changed unknown-field error format; update classifyDecodeError — got %q", err.Error())

	// Verify field name extraction works (same logic as classifyDecodeError).
	field := strings.Trim(after, `"`)
	assert.Equal(t, "bogus", field,
		"field extraction failed; CutPrefix+Trim logic may need updating — got %q", field)
}

func TestClassifyDecodeError_UnknownError(t *testing.T) {
	// Exercise the default branch in classifyDecodeError: an error that is
	// not io.EOF, io.ErrUnexpectedEOF, MaxBytesError, SyntaxError, or
	// UnmarshalTypeError.
	err := classifyDecodeError(errors.New("some obscure decoder error"))

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)
	assert.Equal(t, "internal server error", ecErr.Message)
	assert.NotNil(t, ecErr.Cause, "should wrap the original error")
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

func TestDecodeJSONStrict(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		dst        func() any
		wantCode   errcode.Code
		wantReason string
		wantField  string // expected details["field"] for unknown field errors
	}{
		{
			name:     "valid struct",
			body:     `{"name":"test"}`,
			dst:      func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode: "", // no error
		},
		{
			name:       "unknown field rejected",
			body:       `{"name":"test","extra":"val"}`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "unknown field",
			wantField:  "extra",
		},
		{
			name:       "multiple unknown fields rejects first",
			body:       `{"name":"test","alpha":"a","beta":"b"}`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "unknown field",
			wantField:  "alpha",
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
			name:       "trailing content",
			body:       `{"name":"test"}garbage`,
			dst:        func() any { return &struct{ Name string `json:"name"` }{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
		{
			name:     "map target: unknown fields accepted",
			body:     `{"any":"field","extra":"ok"}`,
			dst:      func() any { return &map[string]json.RawMessage{} },
			wantCode: "", // maps accept any field, even in strict mode
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")

			dst := tt.dst()
			err := DecodeJSONStrict(r, dst)

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
			if tt.wantField != "" {
				assert.Equal(t, tt.wantField, ecErr.Details["field"])
			}
		})
	}
}

func TestDecodeJSONStrict_MaxBytesExceeded(t *testing.T) {
	bigBody := `{"data":"` + strings.Repeat("x", 1024) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(bigBody))
	r.Header.Set("Content-Type", "application/json")
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 10)

	var dst struct {
		Data string `json:"data"`
	}
	err := DecodeJSONStrict(r, &dst)

	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error should be *errcode.Error")
	assert.Equal(t, errcode.ErrBodyTooLarge, ecErr.Code)
}

func TestDecodeJSON_MaxBytesExceeded_TrailingContent(t *testing.T) {
	// Scenario: first JSON value fits within the limit, but the trailing
	// content check (second dec.Decode) reads past it and hits MaxBytesReader.
	//
	// The trailing content is a JSON string literal ("bbb...") so the decoder
	// keeps reading (looking for the closing ") instead of returning a syntax
	// error from the buffer alone.
	firstJSON := `{"n":"x"}`
	largeTrailing := `"` + strings.Repeat("b", 2048) + `"`
	body := firstJSON + largeTrailing

	// Limit: covers the first JSON + some trailing, but not all of it.
	// The decoder's internal buffer receives up to `limit` bytes. After the
	// first decode, the remaining buffer holds a partial JSON string; the
	// second decode exhausts it and calls Read → MaxBytesError.
	limit := int64(len(firstJSON) + 200)

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, limit)

	var dst struct {
		N string `json:"n"`
	}
	err := DecodeJSON(r, &dst)

	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error should be *errcode.Error")
	assert.Equal(t, errcode.ErrBodyTooLarge, ecErr.Code,
		"MaxBytesError during trailing-content check must return ErrBodyTooLarge, not ErrValidationFailed")
}
