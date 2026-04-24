package auditquery

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// newContractQueryHandler builds an http.Handler that registers auditquery
// routes at the canonical API prefix via RegisterRoutes + http.StripPrefix.
// RegisterRoutes calls auth.Mount to install the auditQueryPolicy, so the
// contract test exercises the same guard the production mux uses — not just
// the happy-path handler.
func newContractQueryHandler(entries ...*domain.AuditEntry) http.Handler {
	repo := mem.NewAuditRepository()
	for _, e := range entries {
		_ = repo.Append(context.Background(), e)
	}
	svc, err := NewService(repo, testCodec(), slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	h := NewHandler(svc)
	inner := http.NewServeMux()
	h.RegisterRoutes(inner)
	outer := http.NewServeMux()
	outer.Handle("/api/v1/audit/", http.StripPrefix("/api/v1/audit", inner))
	return outer
}

func TestHttpAuditListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")

	h := newContractQueryHandler(&domain.AuditEntry{
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
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.audit.list.v1")

	h := newContractQueryHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	req = req.WithContext(auth.TestContext("usr-1", nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":"not-array","hasMore":false}`))
}
