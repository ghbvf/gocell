package httputil

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		dst        func() any
		wantCode   errcode.Code
		wantReason string
	}{
		{
			name: "valid struct",
			body: `{"name":"test"}`,
			dst: func() any {
				return &struct {
					Name string `json:"name"`
				}{}
			},
		},
		{
			name: "unknown fields accepted",
			body: `{"name":"test","unknown":"value"}`,
			dst: func() any {
				return &struct {
					Name string `json:"name"`
				}{}
			},
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
			name: "type mismatch",
			body: `{"count":"notanumber"}`,
			dst: func() any {
				return &struct {
					Count int `json:"count"`
				}{}
			},
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "type mismatch",
		},
		{
			name: "trailing content",
			body: `{"name":"test"}garbage`,
			dst: func() any {
				return &struct {
					Name string `json:"name"`
				}{}
			},
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
		{
			name: "trailing close brace",
			body: `{"name":"ok"}}`,
			dst: func() any {
				return &struct {
					Name string `json:"name"`
				}{}
			},
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DecodeJSON(newJSONRequest(tt.body), tt.dst(), DefaultDecodeJSONLimit)
			assertDecodeError(t, err, tt.wantCode, tt.wantReason, "")
		})
	}
}

func TestDecodeJSON_EnforcesExplicitLimit(t *testing.T) {
	bigBody := `{"data":"` + strings.Repeat("x", 1024) + `"}`

	err := DecodeJSON(newJSONRequest(bigBody), &struct {
		Data string `json:"data"`
	}{}, 10)

	assertDecodeError(t, err, errcode.ErrBodyTooLarge, "", "")
}

func TestDecodeJSON_MaxBytesReaderStillClassified(t *testing.T) {
	r := newJSONRequest(`{"data":"` + strings.Repeat("x", 1024) + `"}`)
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 10)

	err := DecodeJSON(r, &struct {
		Data string `json:"data"`
	}{}, DefaultDecodeJSONLimit)

	assertDecodeError(t, err, errcode.ErrBodyTooLarge, "", "")
}

func TestDecodeJSONStrict(t *testing.T) {
	type nestedItem struct {
		ID string `json:"id"`
	}
	type nestedProfile struct {
		Email string `json:"email"`
	}
	type strictRequest struct {
		Name    string        `json:"name"`
		Profile nestedProfile `json:"profile"`
		Items   []nestedItem  `json:"items"`
	}

	tests := []struct {
		name       string
		body       string
		dst        func() any
		wantCode   errcode.Code
		wantReason string
		wantField  string
	}{
		{
			name: "valid struct",
			body: `{"name":"test","profile":{"email":"a@example.test"},"items":[{"id":"i1"}]}`,
			dst:  func() any { return &strictRequest{} },
		},
		{
			name:       "top-level unknown field rejected",
			body:       `{"name":"test","extra":"val"}`,
			dst:        func() any { return &strictRequest{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "unknown field",
			wantField:  "extra",
		},
		{
			name:       "nested unknown field rejected",
			body:       `{"name":"test","profile":{"email":"a@example.test","role":"admin"}}`,
			dst:        func() any { return &strictRequest{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "unknown field",
			wantField:  "profile.role",
		},
		{
			name:       "slice item unknown field rejected",
			body:       `{"name":"test","items":[{"id":"i1","scope":"bad"}]}`,
			dst:        func() any { return &strictRequest{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "unknown field",
			wantField:  "items.scope",
		},
		{
			name: "map target accepts any key",
			body: `{"any":"field","extra":"ok"}`,
			dst:  func() any { return &map[string]string{} },
		},
		{
			name:       "trailing content",
			body:       `{"name":"test"}garbage`,
			dst:        func() any { return &strictRequest{} },
			wantCode:   errcode.ErrValidationFailed,
			wantReason: "trailing content after JSON value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DecodeJSONStrict(newJSONRequest(tt.body), tt.dst(), DefaultDecodeJSONLimit)
			assertDecodeError(t, err, tt.wantCode, tt.wantReason, tt.wantField)
		})
	}
}

func TestDecodeJSONStrict_EnforcesExplicitLimitBeforeReflection(t *testing.T) {
	bigBody := `{"name":"` + strings.Repeat("x", 1024) + `","extra":"rejected by size first"}`

	err := DecodeJSONStrict(newJSONRequest(bigBody), &struct {
		Name string `json:"name"`
	}{}, 10)

	assertDecodeError(t, err, errcode.ErrBodyTooLarge, "", "")
}

func TestClassifyDecodeError_UnknownError(t *testing.T) {
	err := classifyDecodeError(errors.New("some obscure decoder error"))

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)
	assert.Equal(t, "internal server error", ecErr.Message)
	assert.NotNil(t, ecErr.Cause)
}

func newJSONRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func assertDecodeError(t *testing.T, err error, wantCode errcode.Code, wantReason, wantField string) {
	t.Helper()
	if wantCode == "" {
		require.NoError(t, err)
		return
	}

	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, wantCode, ecErr.Code)
	if wantReason != "" {
		reasonAttr, ok := ecErr.FindAttr("reason")
		require.True(t, ok)
		assert.Equal(t, wantReason, reasonAttr.Value.String())
	}
	if wantField != "" {
		fieldAttr, ok := ecErr.FindAttr("field")
		require.True(t, ok)
		assert.Equal(t, wantField, fieldAttr.Value.String())
	}
}
