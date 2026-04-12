package identitymanage

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- stubs ---

type stubOutboxWriter struct{ entries []outbox.Entry }

func (s *stubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

type stubTxRunner struct{ calls int }

func (s *stubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(context.Background())
}

// --- additional handler tests ---

func TestHandler_UpdatePUT(t *testing.T) {
	r := setup()
	// Create a user first.
	w := httptest.NewRecorder()
	body := `{"username":"upd","email":"u@b.com","password":"pass1234"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	id := extractID(t, w.Body.Bytes())

	// PUT update
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/"+id, strings.NewReader(`{"email":"new@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "new@b.com")
}

func TestHandler_UpdatePUT_BadJSON(t *testing.T) {
	r := setup()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/some-id", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_PatchUser(t *testing.T) {
	r := setup()
	// Create a user.
	w := httptest.NewRecorder()
	body := `{"username":"patch","email":"p@b.com","password":"pass1234"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	id := extractID(t, w.Body.Bytes())

	// PATCH name only
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/"+id, strings.NewReader(`{"name":"newname"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "newname")
}

func TestHandler_PatchUser_BadJSON(t *testing.T) {
	r := setup()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/some-id", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_PatchUser_Status(t *testing.T) {
	r := setup()
	// Create + PATCH status
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"st","email":"s@b.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	id := extractID(t, w.Body.Bytes())

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/"+id, strings.NewReader(`{"status":"suspended"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_LockUnlock(t *testing.T) {
	r := setup()
	// Create
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"lock","email":"l@b.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	id := extractID(t, w.Body.Bytes())

	// Lock
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/"+id+"/lock", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "locked")

	// Unlock
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/"+id+"/unlock", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "active")
}

func TestHandler_Lock_NotFound(t *testing.T) {
	r := setup()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/no-such-id/lock", nil)
	r.ServeHTTP(w, req)
	assert.True(t, w.Code >= 400)
}

func TestHandler_Unlock_NotFound(t *testing.T) {
	r := setup()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/no-such-id/unlock", nil)
	r.ServeHTTP(w, req)
	assert.True(t, w.Code >= 400)
}

// --- outbox/tx service tests ---

func TestService_WithOutboxWriter(t *testing.T) {
	ow := &stubOutboxWriter{}
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithOutboxWriter(ow))

	_, err := svc.Create(context.Background(), CreateInput{
		Username: "alice", Email: "a@b.c", Password: "hash",
	})
	require.NoError(t, err)

	assert.Len(t, ow.entries, 1, "outbox should receive user.created event")
	assert.Equal(t, TopicUserCreated, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	tx := &stubTxRunner{}
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithTxManager(tx))

	_, err := svc.Create(context.Background(), CreateInput{
		Username: "alice", Email: "a@b.c", Password: "hash",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls)
}

func TestService_Lock_WithOutbox(t *testing.T) {
	ow := &stubOutboxWriter{}
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithOutboxWriter(ow))

	user, err := svc.Create(context.Background(), CreateInput{
		Username: "bob", Email: "b@c.d", Password: "hash",
	})
	require.NoError(t, err)

	err = svc.Lock(context.Background(), user.ID)
	require.NoError(t, err)

	// One for create, one for lock
	assert.Len(t, ow.entries, 2)
	assert.Equal(t, TopicUserLocked, ow.entries[1].EventType)
}

func TestService_Lock_EmptyID(t *testing.T) {
	svc := newTestService()
	err := svc.Lock(context.Background(), "")
	assert.Error(t, err)
}

func TestService_Unlock_EmptyID(t *testing.T) {
	svc := newTestService()
	err := svc.Unlock(context.Background(), "")
	assert.Error(t, err)
}

func TestService_Delete_EmptyID(t *testing.T) {
	svc := newTestService()
	err := svc.Delete(context.Background(), "")
	assert.Error(t, err)
}

func TestService_Update_EmptyID(t *testing.T) {
	svc := newTestService()
	_, err := svc.Update(context.Background(), UpdateInput{})
	assert.Error(t, err)
}

// --- helpers ---

func extractID(t *testing.T, body []byte) string {
	t.Helper()
	// Quick extraction of id from {"data":{"id":"...",...}}
	type resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	var r resp
	require.NoError(t, json.Unmarshal(body, &r))
	require.NotEmpty(t, r.Data.ID)
	return r.Data.ID
}
