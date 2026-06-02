package correlate_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/correlation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/observability/correlate"
)

// --- handler round-trip helpers ---

func doGet(t *testing.T, handler http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(v); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
}

// --- tests: both params ---

func TestHandler_BothParams_400(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?traceId=abc&cell=xyz")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	assertNoSensitiveFields(t, rec)
}

// TestHandler_BothParams_OneEmpty_400 verifies that providing both keys even
// when one has an empty value is rejected: the key-presence XOR check fires
// before validateParamLength.
func TestHandler_BothParams_OneEmpty_400(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cases := []struct {
		name string
		url  string
	}{
		{"traceId=&cell=x", "/api/v1/observability/correlate?traceId=&cell=x"},
		{"traceId=x&cell=", "/api/v1/observability/correlate?traceId=x&cell="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doGet(t, svc.HTTPHandler(), tc.url)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400; body: %s", rec.Code, rec.Body)
			}
		})
	}
}

// --- tests: neither params ---

func TestHandler_NeitherParam_400(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	assertNoSensitiveFields(t, rec)
}

// TestHandler_PresentEmptyParam_400 verifies that a present-but-empty key
// (?traceId= or ?cell=) is not silently treated as "absent" and re-routed to
// the other mode. The key is present (q.Has returns true), so the routing goes
// to the correct branch; validateParamLength then rejects the empty value.
func TestHandler_PresentEmptyParam_400(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cases := []struct {
		name string
		url  string
	}{
		{"traceId present empty", "/api/v1/observability/correlate?traceId="},
		{"cell present empty", "/api/v1/observability/correlate?cell="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doGet(t, svc.HTTPHandler(), tc.url)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400 for %s; body: %s", rec.Code, tc.name, rec.Body)
			}
		})
	}
}

// --- tests: trace mode ---

func TestHandler_TraceMode_Found_200(t *testing.T) {
	traceID := "trace-handler-001"
	entry := &ledger.Entry{
		ID:            "e1",
		EventID:       "evid-0001-0001-0001-handler001",
		EventType:     "user.login",
		ActorID:       "actor-1",
		SubjectID:     "SENSITIVE-SUBJECT", // must NOT appear in wire body
		CorrelationID: "corr-1",
		TraceID:       traceID,
		SessionID:     "SENSITIVE-SESSION", // must NOT appear in wire body
		OccurredAt:    time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		Timestamp:     time.Date(2026, 1, 1, 10, 0, 1, 0, time.UTC),
	}
	store := newTestStore([]*ledger.Entry{entry}, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?traceId="+traceID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body)
	}

	var resp struct {
		Data struct {
			Query struct {
				TraceID string `json:"traceId"`
			} `json:"query"`
			AuditEntries []struct {
				ID            string `json:"id"`
				EventID       string `json:"eventId"`
				EventType     string `json:"eventType"`
				ActorID       string `json:"actorId"`
				CorrelationID string `json:"correlationId"`
			} `json:"auditEntries"`
			HasMore  *bool `json:"hasMore"`
			Returned *int  `json:"returned"`
		} `json:"data"`
	}
	// Snapshot body before decoding (bytes.Buffer is consumed by Decode).
	bodySnapshot := rec.Body.String()
	rec.Body.Reset()
	rec.Body.WriteString(bodySnapshot)

	decodeJSON(t, rec, &resp)

	if resp.Data.Query.TraceID != traceID {
		t.Errorf("query.traceId: got %q, want %q", resp.Data.Query.TraceID, traceID)
	}
	if len(resp.Data.AuditEntries) != 1 {
		t.Fatalf("auditEntries count: got %d, want 1", len(resp.Data.AuditEntries))
	}
	ae := resp.Data.AuditEntries[0]
	if ae.ID != "e1" {
		t.Errorf("id: got %q, want e1", ae.ID)
	}
	if ae.EventID != entry.EventID {
		t.Errorf("eventId: got %q, want %q", ae.EventID, entry.EventID)
	}
	if ae.EventType != "user.login" {
		t.Errorf("eventType: got %q", ae.EventType)
	}
	// actorId must be present: it is the correlation-relevant triggering principal.
	if ae.ActorID != "actor-1" {
		t.Errorf("actorId: got %q, want actor-1", ae.ActorID)
	}
	if ae.CorrelationID != "corr-1" {
		t.Errorf("correlationId: got %q, want corr-1", ae.CorrelationID)
	}
	// hasMore must be present and false for a single-entry result.
	if resp.Data.HasMore == nil {
		t.Error("hasMore: must be present in trace-mode response, got nil")
	} else if *resp.Data.HasMore {
		t.Error("hasMore: got true for single entry, want false")
	}
	if resp.Data.Returned == nil {
		t.Error("returned: must be present in trace-mode response, got nil")
	} else if *resp.Data.Returned != 1 {
		t.Errorf("returned: got %d, want 1", *resp.Data.Returned)
	}

	// subjectId, sessionId, tenantId, payload must NOT appear in the wire body.
	assertNoSensitiveFieldsInBody(t, bodySnapshot)
	// eventType, actorId, correlationId, eventId must be present in the wire body.
	assertRequiredFieldsInBody(t, bodySnapshot)
	// hasMore must always be present in trace-mode responses.
	assertHasMorePresentInBody(t, bodySnapshot)
}

func TestHandler_TraceMode_NotFound_404(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?traceId=missing")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
	assertNoSensitiveFields(t, rec)
}

func TestHandler_TraceMode_TooLong_400(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 257-byte traceId
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?traceId="+string(long))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

// --- tests: cell mode ---

func TestHandler_CellMode_Found_200(t *testing.T) {
	topo := correlation.Topology{
		"auditcore": correlation.CellOwner{Team: "platform", Role: "audit"},
	}
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, topo, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?cell=auditcore")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body)
	}

	var resp struct {
		Data struct {
			Owner struct {
				CellID string `json:"cellId"`
				Team   string `json:"team"`
				Role   string `json:"role"`
			} `json:"owner"`
			Selectors struct {
				Metric string `json:"metric"`
				Alert  string `json:"alert"`
			} `json:"selectors"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &resp)

	if resp.Data.Owner.CellID != "auditcore" {
		t.Errorf("cellId: got %q", resp.Data.Owner.CellID)
	}
	if resp.Data.Owner.Team != "platform" {
		t.Errorf("team: got %q", resp.Data.Owner.Team)
	}
	if resp.Data.Owner.Role != "audit" {
		t.Errorf("role: got %q", resp.Data.Owner.Role)
	}
	if resp.Data.Selectors.Metric == "" {
		t.Error("selectors.metric must not be empty")
	}
	if resp.Data.Selectors.Alert == "" {
		t.Error("selectors.alert must not be empty")
	}
	assertNoSensitiveFields(t, rec)
}

func TestHandler_CellMode_NotFound_404(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?cell=nosuchcell")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
	assertNoSensitiveFields(t, rec)
}

func TestHandler_CellMode_TooLong_400(t *testing.T) {
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'x'
	}
	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?cell="+string(long))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

// --- security: no sensitive fields in wire body ---

// assertNoSensitiveFields confirms that the response body does not contain
// known sensitive field names. The correlate endpoint deliberately excludes
// subjectId (OAuth sub = end-user PII), tenantId, sessionId, and payload
// from the wire DTO; only the fields needed for trace correlation are returned.
func assertNoSensitiveFields(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertNoSensitiveFieldsInBody(t, rec.Body.String())
}

// assertNoSensitiveFieldsInBody checks a pre-captured body string.
func assertNoSensitiveFieldsInBody(t *testing.T, body string) {
	t.Helper()
	for _, field := range []string{
		"subjectId", "SubjectID",
		"tenantId", "TenantID",
		"sessionId", "SessionID",
		"\"payload\"", "\"Payload\"",
	} {
		if containsSubstring(body, field) {
			t.Errorf("response body must not contain sensitive field %q; body: %s", field, body)
		}
	}
}

// assertRequiredFieldsInBody confirms that the correlation-relevant fields are
// present in the pre-captured body string for a trace-mode 200 response.
// eventId is included because it is a non-PII UUID enabling forward-correlation
// to outbox/logs (C5 fix).
func assertRequiredFieldsInBody(t *testing.T, body string) {
	t.Helper()
	for _, field := range []string{"actorId", "eventType", "correlationId", "eventId"} {
		if !containsSubstring(body, field) {
			t.Errorf("response body must contain field %q; body: %s", field, body)
		}
	}
}

// assertHasMorePresentInBody confirms that the hasMore field is present in the
// trace-mode response wire body. It may be true or false; the field itself must
// always be present so callers can rely on it without checking for key absence.
func assertHasMorePresentInBody(t *testing.T, body string) {
	t.Helper()
	if !containsSubstring(body, "hasMore") {
		t.Errorf("trace-mode response body must contain 'hasMore' field; body: %s", body)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- response envelope shape tests ---

func TestHandler_TraceMode_ResponseEnvelope(t *testing.T) {
	traceID := "trace-envelope-test"
	entry := &ledger.Entry{
		ID:         "e-env",
		EventType:  "test.event",
		TraceID:    traceID,
		OccurredAt: time.Now(),
		Timestamp:  time.Now(),
	}
	store := newTestStore([]*ledger.Entry{entry}, nil)
	svc, err := correlate.NewService(store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?traceId="+traceID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}

	// Confirm top-level "data" wrapper is present
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["data"]; !ok {
		t.Error("response must have top-level 'data' key")
	}
}

func TestHandler_CellMode_ResponseEnvelope(t *testing.T) {
	topo := correlation.Topology{
		"envtest": correlation.CellOwner{Team: "t", Role: "r"},
	}
	store := newTestStore(nil, nil)
	svc, err := correlate.NewService(store, topo, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	rec := doGet(t, svc.HTTPHandler(), "/api/v1/observability/correlate?cell=envtest")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["data"]; !ok {
		t.Error("response must have top-level 'data' key")
	}
}
