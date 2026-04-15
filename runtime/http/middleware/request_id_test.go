package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID_ExistingHeader(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ctxkeys.RequestIDFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "my-request-id", id)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "my-request-id")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	assert.Equal(t, "my-request-id", rec.Header().Get("X-Request-Id"))
}

func TestRequestID_GeneratesUUID(t *testing.T) {
	var capturedID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ctxkeys.RequestIDFrom(r.Context())
		assert.True(t, ok)
		capturedID = id
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.NotEmpty(t, capturedID)
	assert.Len(t, capturedID, 36) // UUID format: 8-4-4-4-12
	assert.Equal(t, capturedID, rec.Header().Get("X-Request-Id"))
}

func TestRequestID_RejectsTooLong(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ctxkeys.RequestIDFrom(r.Context())
		assert.True(t, ok)
		assert.NotEqual(t, "x-long", id[:5], "should not use oversized client ID")
		assert.Len(t, id, 36) // replaced with generated UUID
	}))

	longID := make([]byte, 200)
	for i := range longID {
		longID[i] = 'a'
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", string(longID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestRequestID_RejectsControlChars(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := ctxkeys.RequestIDFrom(r.Context())
		assert.Len(t, id, 36) // replaced with generated UUID
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "evil\nfake-log-entry")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestRequestID_RejectsUnsafeChars(t *testing.T) {
	unsafeIDs := []string{
		`has spaces`,
		`has"quotes`,
		`has{braces}`,
		`has<angle>`,
		"has\ttab",
		`sql' OR '1'='1`,
		`req-123%0Ainjected`,
	}

	for _, unsafeID := range unsafeIDs {
		t.Run(unsafeID, func(t *testing.T) {
			var gotID string
			handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotID, _ = ctxkeys.RequestIDFrom(r.Context())
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("X-Request-Id", unsafeID)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Len(t, gotID, 36, "unsafe ID %q should be replaced with UUID", unsafeID)
		})
	}
}

func TestIsSafeID(t *testing.T) {
	tests := []struct {
		input string
		safe  bool
	}{
		{"abc-123", true},
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"req.trace_id:v1/sub", true},
		{"UPPER-case-Mix", true},
		{"", false},
		{"has space", false},
		{"has\nnewline", false},
		{`has"quote`, false},
		{"has\x00null", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.safe, isSafeID(tt.input))
		})
	}
}

func TestRequestID_BridgesCorrelationID(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corrID, ok := ctxkeys.CorrelationIDFrom(r.Context())
		assert.True(t, ok, "CorrelationID must be present in context")
		assert.Equal(t, "upstream-req-123", corrID,
			"incoming request ID must be bridged to CorrelationID")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "upstream-req-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestRequestID_CorrelationID_MatchesGenerated(t *testing.T) {
	var gotReqID, gotCorrID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqID, _ = ctxkeys.RequestIDFrom(r.Context())
		gotCorrID, _ = ctxkeys.CorrelationIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEmpty(t, gotReqID)
	assert.Equal(t, gotReqID, gotCorrID,
		"when no incoming request ID, generated ID must be used as both RequestID and CorrelationID")
}

func TestRequestID_CorrelationID_InvalidHeader(t *testing.T) {
	var gotReqID, gotCorrID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqID, _ = ctxkeys.RequestIDFrom(r.Context())
		gotCorrID, _ = ctxkeys.CorrelationIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "evil\nfake-log")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Len(t, gotReqID, 36, "should have generated UUID")
	assert.Equal(t, gotReqID, gotCorrID,
		"CorrelationID must match the newly generated RequestID")
}

func TestRequestID_UniquenessAcrossRequests(t *testing.T) {
	ids := make(map[string]bool)
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	for range 100 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		id := rec.Header().Get("X-Request-Id")
		assert.False(t, ids[id], "duplicate request ID: %s", id)
		ids[id] = true
	}
}

// --- Trust boundary tests for RequestIDWithOptions ---

func TestRequestIDWithOptions_PublicEndpoint_IgnoresClientHeader(t *testing.T) {
	isPublic := func(r *http.Request) bool { return r.URL.Path == "/public" }

	var gotID string
	handler := RequestIDWithOptions(
		WithReqIDPublicEndpointFn(isPublic),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = ctxkeys.RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("X-Request-Id", "attacker-supplied-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEqual(t, "attacker-supplied-id", gotID,
		"public endpoint must NOT accept client-supplied X-Request-Id")
	assert.Len(t, gotID, 36, "public endpoint must generate fresh UUID")
}

func TestRequestIDWithOptions_NonPublicEndpoint_AcceptsClientHeader(t *testing.T) {
	isPublic := func(r *http.Request) bool { return r.URL.Path == "/public" }

	var gotID string
	handler := RequestIDWithOptions(
		WithReqIDPublicEndpointFn(isPublic),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = ctxkeys.RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("X-Request-Id", "trusted-upstream-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "trusted-upstream-id", gotID,
		"non-public endpoint must accept valid client X-Request-Id")
}

func TestRequestIDWithOptions_NilPublicEndpointFn_BackwardCompat(t *testing.T) {
	var gotID string
	handler := RequestIDWithOptions()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = ctxkeys.RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/any", nil)
	req.Header.Set("X-Request-Id", "legacy-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "legacy-id", gotID,
		"zero options must preserve backward-compatible behavior")
}

func TestRequestIDWithOptions_PublicEndpoint_BridgesCorrelationID(t *testing.T) {
	isPublic := func(r *http.Request) bool { return true }

	var gotReqID, gotCorrID string
	handler := RequestIDWithOptions(
		WithReqIDPublicEndpointFn(isPublic),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqID, _ = ctxkeys.RequestIDFrom(r.Context())
		gotCorrID, _ = ctxkeys.CorrelationIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Header.Set("X-Request-Id", "attacker-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Len(t, gotReqID, 36)
	assert.Equal(t, gotReqID, gotCorrID,
		"generated request ID must be bridged to CorrelationID")
}
