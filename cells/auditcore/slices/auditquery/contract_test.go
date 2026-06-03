package auditquery

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// newContractQueryHandler builds an http.Handler that registers auditquery
// routes under the canonical API prefix. Mux structure mirrors production
// (RouteMux.Route(prefix, handler.RegisterRoutes)) so auth.Mount strips the
// prefix off Contract.Path exactly as chi does — no alias magic required.
// RegisterRoutes calls auth.Mount to install the auditQueryPolicy, so the
// contract test exercises the same guard the production mux uses.
func newContractQueryHandler(entries ...*ledger.Entry) http.Handler {
	p := testProtocol()
	store, err := ledger.NewMemStore(p, clock.Real())
	if err != nil {
		panic("newContractQueryHandler: NewMemStore: " + err.Error())
	}
	for _, e := range entries {
		if err := store.Append(context.Background(), e); err != nil {
			panic("newContractQueryHandler: Append: " + err.Error())
		}
	}
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/audit", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

// testProtocol builds a ledger.Protocol for contract tests.
func testProtocol() *ledger.Protocol {
	ns, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		panic("testProtocol: " + err.Error())
	}
	p, err := ledger.NewProtocol(
		ns,
		[]byte("test-hmac-key-32bytes-long!!!!!!!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		panic("testProtocol: NewProtocol: " + err.Error())
	}
	return p
}

// TestHttpAuditListV1_QueryParamConstraints asserts that every declared query
// param has at least one reject case (CONTRACT-PATH-QUERY-COVERAGE-01
// per-param granularity):
//   - limit (integer minimum: 1, maximum: 500)
//   - cursor (string maxLength: 4096)
//   - actorId (string maxLength: 256)
//   - eventType (string maxLength: 256)
//   - from (string format: date-time, maxLength: 64)
//   - to (string format: date-time, maxLength: 64)
//   - traceId (string maxLength: 256)
func TestHttpAuditListV1_QueryParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")
	c.ValidateQueryParam(t, "limit", "1")
	c.MustRejectQueryParam(t, "limit", "0")                           // violates minimum: 1
	c.MustRejectQueryParam(t, "limit", "501")                         // violates maximum: 500
	c.MustRejectQueryParam(t, "cursor", string(make([]byte, 4097)))   // violates maxLength: 4096
	c.MustRejectQueryParam(t, "actorId", string(make([]byte, 257)))   // violates maxLength: 256
	c.MustRejectQueryParam(t, "subjectId", string(make([]byte, 257))) // violates maxLength: 256
	c.MustRejectQueryParam(t, "eventType", string(make([]byte, 257))) // violates maxLength: 256
	c.MustRejectQueryParam(t, "traceId", string(make([]byte, 257)))   // violates maxLength: 256
	c.MustRejectQueryParam(t, "from", "not-a-date-time")              // violates format: date-time
	c.MustRejectQueryParam(t, "to", "2026-01-01T00:00:00Z-garbage")   // violates format: date-time
}

func TestHttpAuditListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")

	h := newContractQueryHandler(&ledger.Entry{
		ID: "ae-1", EventID: "evt-1", EventType: "event.test.v1",
		ActorID: "usr-1", Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Payload: []byte(`{"key":"value"}`),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	req = req.WithContext(auth.TestContext("usr-1", nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestHttpAuditListV1Serve_PrincipalProjection pins the auditquery output
// policy (issue #1229 §4 + review F6/F7c). It is the regression lock for two
// invariants the DTO must hold simultaneously:
//
//   - PRESENT: subjectId and correlationId are surfaced; occurredAt is surfaced
//     at RFC3339Nano (sub-second) precision — RFC3339 truncation would drop
//     chain-relevant resolution (F6). correlationId is an opaque cross-cell
//     observability id (NOT in pkg/redaction's sensitive-key set), so it is
//     safe to project (issue #1219).
//   - ABSENT: sessionId and tenantId never appear on the wire, by VALUE or by
//     KEY, even when the underlying ledger.Entry carries them. sessionId is a
//     credential-adjacent token (pkg/redaction sensitive-key set); tenantId now
//     HAS a producer source (epic #1337 PR-2a) and the read path IS tenant-scoped
//     (AuditFilters.TenantID), but a per-row tenantId is redundant — every
//     returned row already belongs to the caller's own tenant — so it is
//     deliberately not projected. The caller below carries the same tenant as the
//     seeded row so the row survives the mandatory tenant scope and the
//     value-leak assertion stays meaningful. Asserting on the raw JSON (not the
//     typed DTO) is deliberate: a future PR that adds the fields to
//     ResponseDataItem would compile-pass but fail here. The audit-domain codegen
//     funnel (contractgen) is the upstream Hard backstop: it rejects any
//     sensitive-key field name in an audit wire-out schema at generation time.
func TestHttpAuditListV1Serve_PrincipalProjection(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")

	// Sub-second OccurredAt so the precision assertion is meaningful.
	occurred := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	h := newContractQueryHandler(&ledger.Entry{
		ID: "ae-proj", EventID: "evt-proj", EventType: "event.test.v1",
		ActorID:       "usr-actor",
		SubjectID:     "sub-of-record",
		TenantID:      "tenant-must-not-leak",
		SessionID:     "session-must-not-leak",
		CorrelationID: "corr-id-123",
		TraceID:       "trace-proj-001",
		OccurredAt:    occurred,
		Timestamp:     time.Date(2026, 1, 2, 3, 4, 6, 987654321, time.UTC),
		Payload:       []byte(`{"key":"value"}`),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	// Caller carries the same tenant as the seeded row so it survives the
	// mandatory tenant scope (epic #1337 PR-2a) and the value-leak assertion below
	// operates on a populated response.
	req = req.WithContext(auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    "usr-actor",
		TenantID:   "tenant-must-not-leak",
		AuthMethod: "test",
	}))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	body := rec.Body.String()

	// PRESENT — subjectId + correlationId + traceId surfaced.
	var resp struct {
		Data []struct {
			SubjectID     string `json:"subjectId"`
			CorrelationID string `json:"correlationId"`
			TraceID       string `json:"traceId"`
			OccurredAt    string `json:"occurredAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, body)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("want 1 row, got %d\nbody=%s", len(resp.Data), body)
	}
	if resp.Data[0].SubjectID != "sub-of-record" {
		t.Errorf("subjectId = %q, want %q", resp.Data[0].SubjectID, "sub-of-record")
	}
	if resp.Data[0].CorrelationID != "corr-id-123" {
		t.Errorf("correlationId = %q, want %q", resp.Data[0].CorrelationID, "corr-id-123")
	}
	if resp.Data[0].TraceID != "trace-proj-001" {
		t.Errorf("traceId = %q, want %q", resp.Data[0].TraceID, "trace-proj-001")
	}
	// PRESENT — occurredAt at nanosecond precision (F6).
	if want := occurred.Format(time.RFC3339Nano); resp.Data[0].OccurredAt != want {
		t.Errorf("occurredAt = %q, want %q (RFC3339Nano sub-second precision)", resp.Data[0].OccurredAt, want)
	}
	if !strings.Contains(resp.Data[0].OccurredAt, ".123456789") {
		t.Errorf("occurredAt %q lost sub-second precision — RFC3339Nano expected", resp.Data[0].OccurredAt)
	}

	// PRESENT — pin the camelCase wire name for traceId (raw-body check).
	if !strings.Contains(body, "traceId") {
		t.Errorf("response body missing %q key — traceId must appear on the wire\nbody=%s", "traceId", body)
	}

	// ABSENT — sessionId / tenantId must not appear by key or by value, in
	// camelCase (DTO/wire) or snake_case (DB column) form, even though the
	// underlying ledger.Entry carries them.
	for _, forbidden := range []string{
		"sessionId", "session_id", "tenantId", "tenant_id",
		"session-must-not-leak", "tenant-must-not-leak",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response leaked %q — sessionId/tenantId must never reach the wire\nbody=%s", forbidden, body)
		}
	}
}

func TestHttpAuditListV1Serve_Empty(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")

	h := newContractQueryHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	req = req.WithContext(auth.TestContext("usr-1", nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":"not-array","hasMore":false}`))
}

func TestHttpAuditListV1_QueryParamsMetadata(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")
	if c.HTTP == nil {
		t.Fatal("HTTP transport metadata should be loaded")
	}

	want := map[string]string{
		"actorId":   "string",
		"cursor":    "string",
		"eventType": "string",
		"from":      "string",
		"limit":     "integer",
		"subjectId": "string",
		"to":        "string",
		"traceId":   "string",
	}
	if len(c.HTTP.QueryParams) != len(want) {
		t.Fatalf("queryParams count = %d, want %d (%v)", len(c.HTTP.QueryParams), len(want), want)
	}
	for name, wantType := range want {
		param, ok := c.HTTP.QueryParams[name]
		if !ok {
			t.Fatalf("queryParams missing %q", name)
		}
		if param.Type != wantType {
			t.Fatalf("queryParams.%s.type = %q, want %q", name, param.Type, wantType)
		}
	}
	if got := c.HTTP.QueryParams["cursor"].MaxLength; got == nil || *got != query.MaxCursorTokenBytes {
		t.Fatalf("queryParams.cursor.maxLength = %v, want %d", got, query.MaxCursorTokenBytes)
	}
	if got := c.HTTP.QueryParams["limit"].Maximum; got == nil || *got != query.MaxPageSize {
		t.Fatalf("queryParams.limit.maximum = %v, want %d", got, query.MaxPageSize)
	}
	if got := c.HTTP.QueryParams["from"].Format; got != "date-time" {
		t.Fatalf("queryParams.from.format = %q, want date-time", got)
	}
	if got := c.HTTP.QueryParams["to"].Format; got != "date-time" {
		t.Fatalf("queryParams.to.format = %q, want date-time", got)
	}
}
