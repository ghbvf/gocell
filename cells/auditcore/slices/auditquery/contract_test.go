package auditquery

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ns),
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
func TestHttpAuditListV1_QueryParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")
	c.ValidateQueryParam(t, "limit", "1")
	c.MustRejectQueryParam(t, "limit", "0")                            // violates minimum: 1
	c.MustRejectQueryParam(t, "limit", "501")                          // violates maximum: 500
	c.MustRejectQueryParam(t, "cursor", string(make([]byte, 4097)))    // violates maxLength: 4096
	c.MustRejectQueryParam(t, "actorId", string(make([]byte, 257)))    // violates maxLength: 256
	c.MustRejectQueryParam(t, "eventType", string(make([]byte, 257)))  // violates maxLength: 256
	c.MustRejectQueryParam(t, "from", "not-a-date-time")               // violates format: date-time
	c.MustRejectQueryParam(t, "to", "2026-01-01T00:00:00Z-garbage")    // violates format: date-time
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
		"to":        "string",
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
