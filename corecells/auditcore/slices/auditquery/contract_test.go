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
	"github.com/ghbvf/gocell/kernel/outbox"
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
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
	req = req.WithContext(auditTestCtx("usr-1", nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestHttpAuditListV1Serve_PrincipalProjection pins the auditquery output policy
// after epic #1337 PR-12 (column masking via the sealed ResourceProjection). The
// caller is a NON-ADMIN user (RowScopeSelf), so auditFieldMask masks the
// operator-diagnostic columns. It is the regression lock for:
//
//   - PRESENT/visible: subjectId (a self caller sees the subject-of-record);
//     tenantId is NOW surfaced too — PR-12 reversed the earlier "omitted as
//     redundant" stance (it is a maskable column behind the funnel, not a
//     pkg/redaction sensitive-key); occurredAt at RFC3339Nano (sub-second)
//     precision (F6); scope marks the own-tenant row.
//   - MASKED: correlationId and traceId are "<REDACTED>" for a non-admin (self)
//     caller — present on the wire (the projection does not fission the response
//     shape) but value-masked. An admin sees them in full (RowScope matrix in
//     handler_test.go). The raw values must NOT leak.
//   - ABSENT: sessionId never appears, by key or value — it is not a projected
//     column at all (the AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 codegen guard keeps
//     the credential-adjacent token out of the response schema).
//
// Asserting on the raw JSON (not the typed DTO) is deliberate: it pins the wire
// bytes a consumer actually receives, masked sentinel included.
func TestHttpAuditListV1Serve_PrincipalProjection(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")

	// Sub-second OccurredAt so the precision assertion is meaningful.
	occurred := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	// auditQueryTestTenant is a canonical UUID — tenant.ParseTenantID requires it.
	// We assert that this UUID does not appear on the wire as a per-row tenantId
	// field (it is deliberately NOT projected by toListResponseDataItem).
	const projTenant = auditQueryTestTenant
	h := newContractQueryHandler(&ledger.Entry{
		ID: "ae-proj", EventID: "evt-proj", EventType: "event.test.v1",
		ActorID:       "usr-actor",
		SubjectID:     "sub-of-record",
		TenantID:      projTenant,
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
		TenantID:   projTenant,
		AuthMethod: "test",
	}))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	body := rec.Body.String()

	const masked = "<REDACTED>" // pkg/redaction.Mask — value-masked column sentinel.

	var resp struct {
		Data []struct {
			SubjectID     string `json:"subjectId"`
			TenantID      string `json:"tenantId"`
			CorrelationID string `json:"correlationId"`
			TraceID       string `json:"traceId"`
			OccurredAt    string `json:"occurredAt"`
			Scope         string `json:"scope"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, body)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("want 1 row, got %d\nbody=%s", len(resp.Data), body)
	}
	row := resp.Data[0]
	// VISIBLE — subjectId is not masked for a self caller (it is the subject of the
	// caller's own audited action).
	if row.SubjectID != "sub-of-record" {
		t.Errorf("subjectId = %q, want %q (visible to self)", row.SubjectID, "sub-of-record")
	}
	// PRESENT — tenantId is now projected (PR-12 forbidden→present migration). It is
	// not in the self-scope mask, so the own-tenant value is visible to the caller.
	if row.TenantID != projTenant {
		t.Errorf("tenantId = %q, want %q (projected, visible to own-tenant self caller)", row.TenantID, projTenant)
	}
	// MASKED — correlationId / traceId are operator-diagnostic columns the self
	// scope masks. Present on the wire (no shape fission), value = redaction sentinel.
	if row.CorrelationID != masked {
		t.Errorf("correlationId = %q, want %q (masked for non-admin self)", row.CorrelationID, masked)
	}
	if row.TraceID != masked {
		t.Errorf("traceId = %q, want %q (masked for non-admin self)", row.TraceID, masked)
	}
	// PRESENT — scope marks this as a tenant-owned row (#1618 review F7).
	if row.Scope != "tenant" {
		t.Errorf("scope = %q, want %q (own-tenant row)", row.Scope, "tenant")
	}
	// PRESENT — occurredAt at nanosecond precision (F6).
	if want := occurred.Format(time.RFC3339Nano); row.OccurredAt != want {
		t.Errorf("occurredAt = %q, want %q (RFC3339Nano sub-second precision)", row.OccurredAt, want)
	}
	if !strings.Contains(row.OccurredAt, ".123456789") {
		t.Errorf("occurredAt %q lost sub-second precision — RFC3339Nano expected", row.OccurredAt)
	}

	// The raw correlationId/traceId VALUES must not leak — they are value-masked.
	for _, leak := range []string{"corr-id-123", "trace-proj-001"} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaked raw masked value %q — must be %q for non-admin self\nbody=%s", leak, masked, body)
		}
	}

	// ABSENT — sessionId must never appear, by key or value: it is not a projected
	// column (the AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 codegen guard keeps the
	// credential-adjacent token out of the response schema entirely).
	for _, forbidden := range []string{"sessionId", "session_id", "session-must-not-leak"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response leaked %q — sessionId must never reach the wire\nbody=%s", forbidden, body)
		}
	}
}

func TestHttpAuditListV1Serve_Empty(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")

	h := newContractQueryHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	req = req.WithContext(auditTestCtx("usr-1", nil))
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
