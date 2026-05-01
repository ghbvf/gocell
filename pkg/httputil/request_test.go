package httputil

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestParsePageParams_Defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items", nil)
	pr, err := ParsePageParams(r)
	require.NoError(t, err)
	assert.Equal(t, query.DefaultPageSize, pr.Limit)
	assert.Empty(t, pr.Cursor)
}

func TestParsePageParams_CustomLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=100", nil)
	pr, err := ParsePageParams(r)
	require.NoError(t, err)
	assert.Equal(t, 100, pr.Limit)
}

func TestParsePageParams_MaxLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=500", nil)
	pr, err := ParsePageParams(r)
	require.NoError(t, err)
	assert.Equal(t, 500, pr.Limit)
}

func TestParsePageParams_ExceedsMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=501", nil)
	_, err := ParsePageParams(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrPageSizeExceeded, ecErr.Code)
}

func TestParsePageParams_ZeroLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=0", nil)
	_, err := ParsePageParams(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

func TestParsePageParams_NegativeLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=-1", nil)
	_, err := ParsePageParams(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

func TestParsePageParams_NonNumericLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=abc", nil)
	_, err := ParsePageParams(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

func TestParsePageParams_WithCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?cursor=TOKEN123", nil)
	pr, err := ParsePageParams(r)
	require.NoError(t, err)
	assert.Equal(t, query.DefaultPageSize, pr.Limit)
	assert.Equal(t, "TOKEN123", pr.Cursor)
}

func TestParsePageParams_LimitAndCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/items?limit=20&cursor=TOKEN", nil)
	pr, err := ParsePageParams(r)
	require.NoError(t, err)
	assert.Equal(t, 20, pr.Limit)
	assert.Equal(t, "TOKEN", pr.Cursor)
}

// TestParsePageParams_CursorTooLong rejects cursors longer than
// query.MaxCursorTokenBytes at the HTTP parse boundary, before any
// base64/HMAC work — defense against DoS amplification via oversize cursors.
// ref: kubernetes apiserver 4 KiB continue-token guidance; enforcing at the
// parse boundary (not only at codec.Decode) avoids wasting work in handlers
// that forward the cursor through layers before decoding.
func TestParsePageParams_CursorTooLong(t *testing.T) {
	oversize := strings.Repeat("A", query.MaxCursorTokenBytes+1)
	u := "/api/v1/items?cursor=" + url.QueryEscape(oversize)
	r := httptest.NewRequest(http.MethodGet, u, nil)

	_, err := ParsePageParams(r)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

// TestParsePageParams_CursorAtMaxLength accepts a cursor at exactly the
// limit (only tokens strictly longer than the cap are rejected at parse time).
func TestParsePageParams_CursorAtMaxLength(t *testing.T) {
	atLimit := strings.Repeat("A", query.MaxCursorTokenBytes)
	u := "/api/v1/items?cursor=" + url.QueryEscape(atLimit)
	r := httptest.NewRequest(http.MethodGet, u, nil)

	pr, err := ParsePageParams(r)
	require.NoError(t, err)
	assert.Len(t, pr.Cursor, query.MaxCursorTokenBytes)
}
