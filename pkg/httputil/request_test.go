package httputil

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePageRequest_Defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items", nil)
	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Equal(t, query.DefaultPageSize, pr.Limit)
	assert.Empty(t, pr.Cursor)
}

func TestParsePageRequest_CustomLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=100", nil)
	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Equal(t, 100, pr.Limit)
}

func TestParsePageRequest_MaxLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=500", nil)
	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Equal(t, 500, pr.Limit)
}

func TestParsePageRequest_ExceedsMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=501", nil)
	_, err := ParsePageRequest(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrPageSizeExceeded, ecErr.Code)
}

func TestParsePageRequest_ZeroLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=0", nil)
	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Equal(t, query.DefaultPageSize, pr.Limit)
}

func TestParsePageRequest_NegativeLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=-1", nil)
	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Equal(t, query.DefaultPageSize, pr.Limit)
}

func TestParsePageRequest_NonNumericLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=abc", nil)
	_, err := ParsePageRequest(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

func TestParsePageRequest_WithCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?cursor=TOKEN123", nil)
	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Equal(t, query.DefaultPageSize, pr.Limit)
	assert.Equal(t, "TOKEN123", pr.Cursor)
}

func TestParsePageRequest_LimitAndCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=20&cursor=TOKEN", nil)
	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Equal(t, 20, pr.Limit)
	assert.Equal(t, "TOKEN", pr.Cursor)
}

// TestParsePageRequest_CursorTooLong rejects cursors longer than
// query.MaxCursorTokenBytes at the HTTP parse boundary, before any
// base64/HMAC work — defense against DoS amplification via oversize cursors.
// ref: kubernetes apiserver 4 KiB continue-token guidance; enforcing at the
// parse boundary (not only at codec.Decode) avoids wasting work in handlers
// that forward the cursor through layers before decoding.
func TestParsePageRequest_CursorTooLong(t *testing.T) {
	oversize := strings.Repeat("A", query.MaxCursorTokenBytes+1)
	u := "/api/v1/items?cursor=" + url.QueryEscape(oversize)
	r := httptest.NewRequest(http.MethodGet, u, nil)

	_, err := ParsePageRequest(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

// TestParsePageRequest_CursorAtMaxLength accepts a cursor at exactly the
// limit (only tokens strictly longer than the cap are rejected at parse time).
func TestParsePageRequest_CursorAtMaxLength(t *testing.T) {
	atLimit := strings.Repeat("A", query.MaxCursorTokenBytes)
	u := "/api/v1/items?cursor=" + url.QueryEscape(atLimit)
	r := httptest.NewRequest(http.MethodGet, u, nil)

	pr, err := ParsePageRequest(r)
	require.NoError(t, err)
	assert.Len(t, pr.Cursor, query.MaxCursorTokenBytes)
}
