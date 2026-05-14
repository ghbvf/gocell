package identitymanage

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/runtime/auth"
)

// --- stubs ---

type stubOutboxWriter struct {
	entries []outbox.Entry
	err     error // when set, Write returns this error
}

func (s *stubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, e)
	return nil
}

type stubTxRunner struct{ calls int }

func (s *stubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(context.Background())
}

// outboxStubIssuer is a minimal TokenIssuer stub used by outbox tests that do
// not exercise the ChangePassword token-issuing path.
var outboxStubIssuer TokenIssuer = &stubTokenIssuer{}

// --- additional handler tests ---

func withAdmin(req *http.Request) *http.Request {
	return req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
}

func TestHandler_UpdatePUT(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	body := `{"username":"upd","email":"u@b.com","password":"pass1234"}`
	req := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix, strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	id := extractID(t, w.Body.Bytes())

	w = httptest.NewRecorder()
	req = withAdmin(httptest.NewRequest(http.MethodPut, identityPrefix+"/"+id, strings.NewReader(`{"email":"new@b.com"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "new@b.com")
}

func TestHandler_UpdatePUT_BadJSON(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPut, identityPrefix+"/"+testutil.TestID("some-id"), strings.NewReader("{bad")))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_PatchUser(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	body := `{"username":"patch","email":"p@b.com","password":"pass1234"}`
	req := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix, strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	id := extractID(t, w.Body.Bytes())

	w = httptest.NewRecorder()
	req = withAdmin(httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+id, strings.NewReader(`{"name":"newname"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "newname")
}

func TestHandler_PatchUser_BadJSON(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPatch, identityPrefix+"/some-id", strings.NewReader("{bad")))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_PatchUser_Status(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"st","email":"s@b.com","password":"pass1234"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	id := extractID(t, w.Body.Bytes())

	w = httptest.NewRecorder()
	req = withAdmin(httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+id, strings.NewReader(`{"status":"suspended"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_LockUnlock(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"lock","email":"l@b.com","password":"pass1234"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	id := extractID(t, w.Body.Bytes())

	// Lock/Unlock send an empty JSON body ({}) — the generated handler calls
	// DecodeJSONStrict so the request must have Content-Type: application/json.
	w = httptest.NewRecorder()
	lockReq := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix+"/"+id+"/lock", strings.NewReader("{}")))
	lockReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, lockReq)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "locked")

	w = httptest.NewRecorder()
	unlockReq := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix+"/"+id+"/unlock", strings.NewReader("{}")))
	unlockReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, unlockReq)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "active")
}

func TestHandler_Lock_NotFound(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix+"/"+testutil.TestID("no-such-id")+"/lock", strings.NewReader("{}")))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_Unlock_NotFound(t *testing.T) {
	r := setup(t)
	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, identityPrefix+"/"+testutil.TestID("no-such-id")+"/unlock", strings.NewReader("{}")))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- outbox/tx service tests ---

func TestService_WithEmitter(t *testing.T) {
	ow := &stubOutboxWriter{}
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(userRepo, newInvalidator(t, userRepo, sessionStore, refreshStore), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTokenIssuer(outboxStubIssuer), WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	_, err = svc.Create(adminCtxForService(), CreateInput{
		Username: "alice", Email: "a@b.c", Password: "hash",
	})
	require.NoError(t, err)

	assert.Len(t, ow.entries, 1, "outbox should receive user.created event")
	assert.Equal(t, TopicUserCreated, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	tx := &stubTxRunner{}
	txUserRepo := mem.NewStore(clock.Real()).UserRepository()
	txSessionStore := testutil.RealSessionRepo(t)
	txRefreshStore := newIdentityRefreshStore()
	svc, err := NewService(txUserRepo, newInvalidator(t, txUserRepo, txSessionStore, txRefreshStore), slog.Default(),
		WithTxManager(persistence.WrapForCell(tx)), WithTokenIssuer(outboxStubIssuer), WithClock(clock.Real()))
	require.NoError(t, err)

	_, err = svc.Create(adminCtxForService(), CreateInput{
		Username: "alice", Email: "a@b.c", Password: "hash",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls)
}

func TestService_Lock_WithOutbox(t *testing.T) {
	ow := &stubOutboxWriter{}
	lockUserRepo := mem.NewStore(clock.Real()).UserRepository()
	lockSessionStore := testutil.RealSessionRepo(t)
	lockRefreshStore := newIdentityRefreshStore()
	svc, err := NewService(lockUserRepo, newInvalidator(t, lockUserRepo, lockSessionStore, lockRefreshStore), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTokenIssuer(outboxStubIssuer), WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "bob", Email: "b@c.d", Password: "hash",
	})
	require.NoError(t, err)

	err = svc.Lock(auth.TestContext("test-admin", []string{"admin"}), user.ID)
	require.NoError(t, err)

	// One for create, one for lock
	assert.Len(t, ow.entries, 2)
	assert.Equal(t, TopicUserLocked, ow.entries[1].EventType)
}

func TestService_Lock_EmptyID(t *testing.T) {
	svc := newTestService(t)
	err := svc.Lock(auth.TestContext("test-admin", []string{"admin"}), "")
	assert.Error(t, err)
}

func TestService_Unlock_EmptyID(t *testing.T) {
	svc := newTestService(t)
	err := svc.Unlock(auth.TestContext("test-admin", []string{"admin"}), "")
	assert.Error(t, err)
}

func TestService_Delete_EmptyID(t *testing.T) {
	svc := newTestService(t)
	err := svc.Delete(auth.TestContext("test-admin", []string{"admin"}), "")
	assert.Error(t, err)
}

func TestService_Update_EmptyID(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Update(auth.TestContext("test-admin", []string{"admin"}), UpdateInput{})
	assert.Error(t, err)
}

// --- #27d OUTBOX-WRITE-ERR-01: outbox.Write error must propagate ---

func TestService_Create_OutboxWriteError(t *testing.T) {
	ow := &stubOutboxWriter{err: errors.New("outbox unavailable")}
	createUserRepo := mem.NewStore(clock.Real()).UserRepository()
	createSessionStore := testutil.RealSessionRepo(t)
	createRefreshStore := newIdentityRefreshStore()
	svc, err := NewService(createUserRepo, newInvalidator(t, createUserRepo, createSessionStore, createRefreshStore), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTokenIssuer(outboxStubIssuer), WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	_, err = svc.Create(adminCtxForService(), CreateInput{
		Username: "alice", Email: "a@b.c", Password: "hash",
	})
	require.Error(t, err, "Create must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Lock_OutboxWriteError(t *testing.T) {
	repo := mem.NewStore(clock.Real()).UserRepository()
	// Create user with working outbox
	createSessionStore := testutil.RealSessionRepo(t)
	createRefreshStore := newIdentityRefreshStore()
	svcCreate, err := NewService(repo, newInvalidator(t, repo, createSessionStore, createRefreshStore),
		slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, &stubOutboxWriter{})), WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithTokenIssuer(outboxStubIssuer), WithClock(clock.Real()))
	require.NoError(t, err)
	user, err := svcCreate.Create(adminCtxForService(), CreateInput{
		Username: "bob", Email: "b@c.d", Password: "hash",
	})
	require.NoError(t, err)

	// Lock with failing outbox
	failWriter := &stubOutboxWriter{err: errors.New("outbox unavailable")}
	lockSessionStore := testutil.RealSessionRepo(t)
	lockRefreshStore := newIdentityRefreshStore()
	svcLock, err := NewService(repo, newInvalidator(t, repo, lockSessionStore, lockRefreshStore),
		slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, failWriter)), WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithTokenIssuer(outboxStubIssuer), WithClock(clock.Real()))
	require.NoError(t, err)

	err = svcLock.Lock(auth.TestContext("test-admin", []string{"admin"}), user.ID)
	require.Error(t, err, "Lock must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
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
