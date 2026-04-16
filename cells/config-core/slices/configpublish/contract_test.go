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
