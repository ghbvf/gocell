package flagwrite

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAdminSubject = "admin-test"

// newContractMux registers flagwrite routes on a mux at the canonical API prefix.
func newContractMux(svc *Service) *http.ServeMux {
	h := NewHandler(svc)
	mux := http.NewServeMux()
	// Register each route individually to avoid redirect issues with StripPrefix.
	mux.Handle("POST /api/v1/flags", auth.Secured(h.HandleCreate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("PUT /api/v1/flags/{key}", auth.Secured(h.HandleUpdate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("POST /api/v1/flags/{key}/toggle", auth.Secured(h.HandleToggle, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("DELETE /api/v1/flags/{key}", auth.Secured(h.HandleDelete, auth.AnyRole(dto.RoleAdmin)))
	return mux
}

func newContractService() *Service {
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	return NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
}

// --- Create contract test ---

func TestHttpConfigFlagsCreateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.flags.create.v1")
	svc := newContractService()
	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"key":"my-flag","enabled":false,"rolloutPercentage":0,"description":"test"}`))
	c.MustRejectRequest(t, []byte(`{"key":"k","extra":"field"}`))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path,
		strings.NewReader(`{"key":"my-flag","enabled":false,"rolloutPercentage":0,"description":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{dto.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Update contract test ---

func TestHttpConfigFlagsUpdateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.flags.update.v1")
	svc := newContractService()

	// Seed a flag first.
	_, err := svc.Create(testAdminCtx(), CreateInput{Key: "upd-flag", Description: "seed"})
	require.NoError(t, err)

	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"enabled":true,"rolloutPercentage":50,"description":"updated"}`))
	c.MustRejectRequest(t, []byte(`{"enabled":true,"extra":"bad"}`))

	path := strings.ReplaceAll(c.HTTP.Path, "{key}", "upd-flag")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path,
		strings.NewReader(`{"enabled":true,"rolloutPercentage":50,"description":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{dto.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Toggle contract test ---

func TestHttpConfigFlagsToggleV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.flags.toggle.v1")
	svc := newContractService()

	_, err := svc.Create(testAdminCtx(), CreateInput{Key: "tgl-flag", Description: "seed"})
	require.NoError(t, err)

	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"enabled":true}`))
	c.MustRejectRequest(t, []byte(`{"enabled":true,"extra":"bad"}`))

	path := strings.ReplaceAll(c.HTTP.Path, "{key}", "tgl-flag")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path,
		strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{dto.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Delete contract test ---

func TestHttpConfigFlagsDeleteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.flags.delete.v1")
	svc := newContractService()

	_, err := svc.Create(testAdminCtx(), CreateInput{Key: "del-flag", Description: "seed"})
	require.NoError(t, err)

	mux := newContractMux(svc)

	path := strings.ReplaceAll(c.HTTP.Path, "{key}", "del-flag")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, http.NoBody)
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{dto.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body)
}

// --- Event contract test ---

func TestEventFlagChangedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.flag.changed.v1")

	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))

	_, err := svc.Create(testAdminCtx(), CreateInput{Key: "event-flag", Enabled: true, Description: "ev"})
	require.NoError(t, err)

	require.Len(t, writer.entries, 1)
	var payload FlagChangedPayload
	require.NoError(t, json.Unmarshal(writer.entries[0].Payload, &payload))

	c.ValidatePayload(t, writer.entries[0].Payload)
	assert.Equal(t, "created", payload.Action)
	assert.Equal(t, "event-flag", payload.Key)

	// Contract invariant: payload.eventId must equal the transport-level
	// envelope identifier (outbox.Entry.ID) because headers.event_id —
	// declared idempotencyKey in contract.yaml — is carried via Entry.ID.
	// Two independent UUIDs here would let headers-based idempotency drift
	// from payload-based inspection. See headers.schema.json description.
	assert.Equal(t, writer.entries[0].ID, payload.EventID,
		"contract drift: payload.eventId must mirror outbox.Entry.ID so "+
			"headers.event_id (idempotencyKey) is coherent across envelope and body")
}

func testAdminCtx() context.Context {
	return auth.TestContext(testAdminSubject, []string{dto.RoleAdmin})
}
