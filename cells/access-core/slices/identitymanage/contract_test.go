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
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/require"
)

// testPassword is a deterministic credential used only in contract tests.
// Extracted as a constant to satisfy S6437 (no hardcoded credentials).
const testPassword = "contract-test-P@ssw0rd" //nolint:gosec

// --- contract test doubles ---

type contractRecordingWriter struct {
	entries []outbox.Entry
}

func (w *contractRecordingWriter) Write(_ context.Context, e outbox.Entry) error {
	w.entries = append(w.entries, e)
	return nil
}

type contractTxRunner struct{}

func (contractTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = contractTxRunner{}

func setupContractHandler() http.Handler {
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default())
	return buildMux(svc)
}

func setupContractHandlerWithOutbox() (http.Handler, *contractRecordingWriter) {
	writer := &contractRecordingWriter{}
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithOutboxWriter(writer), WithTxManager(contractTxRunner{}))
	return buildMux(svc), writer
}

func buildMux(svc *Service) http.Handler {
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Handle("POST /api/v1/access/users", http.HandlerFunc(h.handleCreate))
	mux.Handle("GET /api/v1/access/users/{id}", http.HandlerFunc(h.handleGet))
	mux.Handle("PUT /api/v1/access/users/{id}", http.HandlerFunc(h.handleUpdate))
	mux.Handle("PATCH /api/v1/access/users/{id}", http.HandlerFunc(h.handlePatch))
	mux.Handle("DELETE /api/v1/access/users/{id}", http.HandlerFunc(h.handleDelete))
	mux.Handle("POST /api/v1/access/users/{id}/lock", http.HandlerFunc(h.handleLock))
	mux.Handle("POST /api/v1/access/users/{id}/unlock", http.HandlerFunc(h.handleUnlock))
	return mux
}

func createUserForContractTest(t *testing.T, handler http.Handler, contract *contracttest.Contract) string {
	t.Helper()
	body := `{"username":"alice","email":"a@b.com","password":"` + testPassword + `"}`
	req := httptest.NewRequest(contract.HTTP.Method, contract.HTTP.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	contract.ValidateHTTPResponseRecorder(t, recorder)

	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if response.Data.ID == "" {
		t.Fatal("create response did not include data.id")
	}
	return response.Data.ID
}

func TestHttpAuthUserCreateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	handler := setupContractHandler()

	c.ValidateRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"`+testPassword+`"}`))
	c.MustRejectRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"s","extra":"bad"}`))

	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"username":"alice","email":"a@b.com","password":"`+testPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	c.ValidateHTTPResponseRecorder(t, recorder)
}

func TestHttpAuthUserGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.get.v1")
	handler := setupContractHandler()

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUpdateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.update.v1")
	handler := setupContractHandler()

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"email":"new@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.ValidateRequest(t, []byte(`{"email":"new@b.com"}`))
	c.MustRejectRequest(t, []byte(`{"email":"a","extra":"bad"}`))
}

func TestHttpAuthUserPatchV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.patch.v1")
	handler := setupContractHandler()

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"name":"Bob"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserDeleteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	deleteContract := contracttest.LoadByID(t, root, "http.auth.user.delete.v1")
	handler := setupContractHandler()

	deleteContract.ValidateRequest(t, []byte(`{}`))
	deleteContract.MustRejectRequest(t, []byte(`{"unexpected":true}`))

	userID := createUserForContractTest(t, handler, createContract)
	deletePath := strings.Replace(deleteContract.HTTP.Path, "{id}", userID, 1)
	req := httptest.NewRequest(deleteContract.HTTP.Method, deletePath, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	deleteContract.ValidateHTTPResponseRecorder(t, recorder)
}

func TestHttpAuthUserLockV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")
	handler := setupContractHandler()

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUnlockV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	lockContract := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.unlock.v1")
	handler := setupContractHandler()

	userID := createUserForContractTest(t, handler, createContract)
	// Lock first
	lockPath := strings.Replace(lockContract.HTTP.Path, "{id}", userID, 1)
	lockReq := httptest.NewRequest(lockContract.HTTP.Method, lockPath, nil)
	handler.ServeHTTP(httptest.NewRecorder(), lockReq)

	// Unlock
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

// --- Event contract tests with real handler output ---

func TestEventUserCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "event.user.created.v1")
	handler, writer := setupContractHandlerWithOutbox()

	_ = createUserForContractTest(t, handler, createContract)

	require.Len(t, writer.entries, 1, "Create must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"user_id":"x"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventUserLockedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	lockContract := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")
	c := contracttest.LoadByID(t, root, "event.user.locked.v1")
	handler, writer := setupContractHandlerWithOutbox()

	userID := createUserForContractTest(t, handler, createContract)
	writer.entries = nil // reset after create event

	lockPath := strings.Replace(lockContract.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(lockContract.HTTP.Method, lockPath, nil)
	handler.ServeHTTP(rec, req)
	lockContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, writer.entries, 1, "Lock must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

// --- #22 DELETE-NOCONTENT-01: 204 semantic negative test ---

func TestHttpAuthUserDeleteV1Serve_RejectsBodyOn204(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.delete.v1")

	// Fabricate a buggy 204 response with a body to prove the contract catches it.
	rec := httptest.NewRecorder()
	rec.WriteHeader(204)
	_, _ = rec.Write([]byte(`{"error":"oops"}`))

	mockT := &capturingTB{}
	c.ValidateHTTPResponseRecorder(mockT, rec)
	if !mockT.errored {
		t.Fatal("204 contract must reject responses with non-empty body")
	}
}

// capturingTB is a minimal testing.TB that captures whether an error method was called.
// Methods beyond Helper/Errorf/Fatalf are implemented defensively to avoid nil-panic
// if ValidateHTTPResponseRecorder's call-sites change.
type capturingTB struct {
	testing.TB
	errored bool
}

func (c *capturingTB) Helper()                            {}
func (c *capturingTB) Errorf(format string, args ...any)  { c.errored = true }
func (c *capturingTB) Fatalf(format string, args ...any)  { c.errored = true }
func (c *capturingTB) Logf(format string, args ...any)    {}
func (c *capturingTB) Name() string                       { return "capturingTB" }
func (c *capturingTB) Log(args ...any)                    {}
func (c *capturingTB) Error(args ...any)                  { c.errored = true }
func (c *capturingTB) Fatal(args ...any)                  { c.errored = true }
