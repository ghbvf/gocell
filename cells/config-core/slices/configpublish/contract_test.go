package configpublish

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/require"
)

func newContractService() (*Service, *mem.ConfigRepository, *recordingWriter) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	return svc, repo, writer
}

func seedContractEntry(repo *mem.ConfigRepository, key, value string) {
	now := time.Now()
	_ = repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
}

// --- HTTP contract test ---

func TestHttpConfigPublishV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.publish.v1")
	svc, repo, _ := newContractService()
	seedContractEntry(repo, "app.name", "value")

	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/config/{key}/publish", http.HandlerFunc(h.HandlePublish))

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{key}", "app.name", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, nil).
		WithContext(auth.TestContext("contract-admin", []string{"admin"}))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"id":"x"}}`))
	// H2-2 redaction guard: response missing the `sensitive` flag must be rejected
	// once the schema requires it (lock-in for redaction-aware contract).
	c.MustRejectResponse(t, []byte(`{"data":{"id":"v","configId":"c","version":1,"value":"plain"}}`))
}

func TestHttpConfigRollbackV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.rollback.v1")
	svc, repo, _ := newContractService()
	seedContractEntry(repo, "app.name", "value")

	// Publish first to create version 1 so rollback target exists.
	_, err := svc.Publish(context.Background(), "app.name")
	require.NoError(t, err)

	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/config/{key}/rollback", http.HandlerFunc(h.HandleRollback))

	// Request schema acceptance + rejection.
	c.ValidateRequest(t, []byte(`{"version":1}`))
	c.MustRejectRequest(t, []byte(`{"version":0}`))
	c.MustRejectRequest(t, []byte(`{"version":"1"}`))
	c.MustRejectRequest(t, []byte(`{}`))
	c.MustRejectRequest(t, []byte(`{"version":1,"extra":"x"}`))

	// Real-handler exercise: 200 OK + response schema.
	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{key}", "app.name", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"version":1}`)).
		WithContext(auth.TestContext("contract-admin", []string{"admin"}))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"id":"x"}}`))
}

// --- Error response contract tests ---

// validErrBody constructs a minimal canonical error body matching pkg/httputil envelope.
func validErrBody(code, msg string) []byte {
	return []byte(`{"error":{"code":"` + code + `","message":"` + msg + `","details":{}}}`)
}

func TestHttpConfigPublishV1_ErrorResponses(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.publish.v1")
	c.ValidateErrorResponse(t, 401, validErrBody("ERR_AUTH_INVALID_TOKEN", "unauthorized"))
	c.ValidateErrorResponse(t, 403, validErrBody("ERR_AUTH_FORBIDDEN", "forbidden"))
}

func TestHttpConfigRollbackV1_ErrorResponses(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.rollback.v1")
	c.ValidateErrorResponse(t, 401, validErrBody("ERR_AUTH_INVALID_TOKEN", "unauthorized"))
	c.ValidateErrorResponse(t, 403, validErrBody("ERR_AUTH_FORBIDDEN", "forbidden"))
}

// TestHttpConfigPublishV1_Serve_Unauthorized exercises the real handler for
// 401 (no auth ctx) and 403 (authenticated but lacks admin role) paths, then
// validates the response body shape against the contract's declared error schema.
func TestHttpConfigPublishV1_Serve_Unauthorized(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.publish.v1")
	svc, _, _ := newContractService()
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/config/{key}/publish", http.HandlerFunc(h.HandlePublish))

	path := strings.Replace(c.HTTP.Path, "{key}", "app.name", 1)

	t.Run("401_no_subject", func(t *testing.T) {
		// context.Background() carries no subject → RequireAnyRole → ErrAuthUnauthorized → 401
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.HTTP.Method, path, nil).
			WithContext(context.Background())
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
	})

	t.Run("403_insufficient_role", func(t *testing.T) {
		// auth.TestContext with a non-admin role → RequireAnyRole → ErrAuthForbidden → 403
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.HTTP.Method, path, nil).
			WithContext(auth.TestContext("user-readonly", []string{"viewer"}))
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code)
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
	})
}

// --- Event contract tests ---

func TestEventConfigChangedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, repo, writer := newContractService()
	seedContractEntry(repo, "app.name", "value")

	_, err := svc.Publish(context.Background(), "app.name")
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Publish must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"action":"published","key":"app.name"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigRollbackV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.rollback.v1")
	svc, repo, writer := newContractService()
	seedContractEntry(repo, "app.name", "v1")

	// Publish first to create a version, then rollback
	_, err := svc.Publish(context.Background(), "app.name")
	require.NoError(t, err)
	writer.entries = nil // reset

	_, err = svc.Rollback(context.Background(), "app.name", 1)
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Rollback must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"action":"rollback","key":"app.name"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
