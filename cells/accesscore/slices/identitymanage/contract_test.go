package identitymanage

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

func newIdentityRefreshStore() refresh.Store {
	clock := storetest.NewFakeClock(time.Now())
	return refreshmem.MustNew(refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clock, nil)
}

// testPassword is a deterministic credential used only in contract tests.
// Extracted as a constant to satisfy S6437 (no hardcoded credentials).
const testPassword = "contract-test-P@ssw0rd"

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

// contractStubIssuer is a minimal TokenIssuer stub for contract tests that do
// not exercise the ChangePassword token-issuing path.
var contractStubIssuer TokenIssuer = &stubTokenIssuer{}

func setupContractHandler(t testing.TB) http.Handler {
	t.Helper()
	svc, err := NewService(mem.NewUserRepository(), testutil.RealSessionRepo(t), newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(contractStubIssuer), WithClock(clock.Real()))
	if err != nil {
		t.Fatalf("setupContractHandler: %v", err)
	}
	return buildMux(svc)
}

func setupContractHandlerWithOutbox(t testing.TB) (http.Handler, *contractRecordingWriter) {
	t.Helper()
	writer := &contractRecordingWriter{}
	svc, err := NewService(mem.NewUserRepository(), testutil.RealSessionRepo(t),
		newIdentityRefreshStore(), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(contractTxRunner{}),
		WithTokenIssuer(contractStubIssuer), WithClock(clock.Real()))
	if err != nil {
		t.Fatalf("setupContractHandlerWithOutbox: %v", err)
	}
	return buildMux(svc), writer
}

// buildMux uses h.RegisterRoutes as the single source of truth for route
// and auth-metadata declarations. TestMux.Route mirrors the production chi
// sub-router structure — auth.Mount strips the canonical API prefix off
// Contract.Path so requests match without any relative-alias magic.
func buildMux(svc *Service) *celltest.TestMux {
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/access/users", func(sub kcell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

func setupContractHandlerWithIssuer(t testing.TB, issuer TokenIssuer) (http.Handler, *mem.UserRepository) {
	t.Helper()
	repo := mem.NewUserRepository()
	svc, err := NewService(repo, testutil.RealSessionRepo(t), newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(issuer), WithClock(clock.Real()))
	if err != nil {
		t.Fatalf("setupContractHandlerWithIssuer: %v", err)
	}
	return buildMux(svc), repo
}

func createUserForContractTest(t *testing.T, handler http.Handler, contract *contracttest.Contract) string {
	t.Helper()
	body := `{"username":"alice","email":"a@b.com","password":"` + testPassword + `"}`
	req := httptest.NewRequest(contract.HTTP.Method, contract.HTTP.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
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
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	handler := setupContractHandler(t)

	c.ValidateRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"`+testPassword+`"}`))
	c.MustRejectRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"s","extra":"bad"}`))

	body := strings.NewReader(`{"username":"alice","email":"a@b.com","password":"` + testPassword + `"}`)
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	c.ValidateHTTPResponseRecorder(t, recorder)
}

func TestHttpAuthUserGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.get.v1")
	handler := setupContractHandler(t)

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUpdateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.update.v1")
	handler := setupContractHandler(t)

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"email":"new@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.ValidateRequest(t, []byte(`{"email":"new@b.com"}`))
	c.MustRejectRequest(t, []byte(`{"email":"a","extra":"bad"}`))
}

func TestHttpAuthUserPatchV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.patch.v1")
	handler := setupContractHandler(t)

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"name":"Bob"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	// H2-3: validate request schema for PATCH (JSON merge patch).
	c.ValidateRequest(t, []byte(`{"name":"Bob"}`))
	c.ValidateRequest(t, []byte(`{"email":"new@b.com"}`))
	c.ValidateRequest(t, []byte(`{"name":"Bob","email":"new@b.com","status":"active"}`))
	c.MustRejectRequest(t, []byte(`{"email":"new@b.com","extra":"ignored"}`))

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserDeleteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	deleteContract := contracttest.LoadByID(t, root, "http.auth.user.delete.v1")
	handler := setupContractHandler(t)

	deleteContract.ValidateRequest(t, []byte(`{}`))
	deleteContract.MustRejectRequest(t, []byte(`{"unexpected":true}`))

	userID := createUserForContractTest(t, handler, createContract)
	deletePath := strings.Replace(deleteContract.HTTP.Path, "{id}", userID, 1)
	req := httptest.NewRequest(deleteContract.HTTP.Method, deletePath, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	deleteContract.ValidateHTTPResponseRecorder(t, recorder)
}

func TestHttpAuthUserLockV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")
	handler := setupContractHandler(t)

	userID := createUserForContractTest(t, handler, createContract)
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUnlockV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	lockContract := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.unlock.v1")
	handler := setupContractHandler(t)

	userID := createUserForContractTest(t, handler, createContract)
	// Lock first
	lockPath := strings.Replace(lockContract.HTTP.Path, "{id}", userID, 1)
	lockReq := httptest.NewRequest(lockContract.HTTP.Method, lockPath, nil)
	lockReq = lockReq.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(httptest.NewRecorder(), lockReq)

	// Unlock
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

// --- Event contract tests with real handler output ---

func TestEventUserCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "event.user.created.v1")
	handler, writer := setupContractHandlerWithOutbox(t)

	_ = createUserForContractTest(t, handler, createContract)

	require.Len(t, writer.entries, 1, "Create must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"user_id":"x"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventUserLockedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	lockContract := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")
	c := contracttest.LoadByID(t, root, "event.user.locked.v1")
	handler, writer := setupContractHandlerWithOutbox(t)

	userID := createUserForContractTest(t, handler, createContract)
	writer.entries = nil // reset after create event

	lockPath := strings.Replace(lockContract.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(lockContract.HTTP.Method, lockPath, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	lockContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, writer.entries, 1, "Lock must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventUserUpdatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	updateContract := contracttest.LoadByID(t, root, "http.auth.user.update.v1")
	c := contracttest.LoadByID(t, root, "event.user.updated.v1")
	handler, writer := setupContractHandlerWithOutbox(t)

	userID := createUserForContractTest(t, handler, createContract)
	writer.entries = nil // reset after create event

	path := strings.Replace(updateContract.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(updateContract.HTTP.Method, path, strings.NewReader(`{"email":"updated@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	updateContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, writer.entries, 1, "Update must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{}`))
}

func TestEventUserDeletedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	deleteContract := contracttest.LoadByID(t, root, "http.auth.user.delete.v1")
	c := contracttest.LoadByID(t, root, "event.user.deleted.v1")
	handler, writer := setupContractHandlerWithOutbox(t)

	userID := createUserForContractTest(t, handler, createContract)
	writer.entries = nil // reset after create event

	deletePath := strings.Replace(deleteContract.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(deleteContract.HTTP.Method, deletePath, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	deleteContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, writer.entries, 1, "Delete must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{}`))
}

func TestEventUserUnlockedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	lockContract := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")
	unlockContract := contracttest.LoadByID(t, root, "http.auth.user.unlock.v1")
	c := contracttest.LoadByID(t, root, "event.user.unlocked.v1")
	handler, writer := setupContractHandlerWithOutbox(t)

	userID := createUserForContractTest(t, handler, createContract)
	writer.entries = nil

	// Lock first
	lockPath := strings.Replace(lockContract.HTTP.Path, "{id}", userID, 1)
	lockReq := httptest.NewRequest(lockContract.HTTP.Method, lockPath, nil)
	lockReq = lockReq.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(httptest.NewRecorder(), lockReq)
	writer.entries = nil // reset after lock event

	// Unlock
	unlockPath := strings.Replace(unlockContract.HTTP.Path, "{id}", userID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(unlockContract.HTTP.Method, unlockPath, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	handler.ServeHTTP(rec, req)
	unlockContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, writer.entries, 1, "Unlock must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{}`))
}

// --- #22 DELETE-NOCONTENT-01: 204 semantic negative test ---

func TestHttpAuthUserDeleteV1Serve_RejectsBodyOn204(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.user.delete.v1")

	// Fabricate a buggy 204 response with a body to prove the contract catches it.
	rec := httptest.NewRecorder()
	rec.WriteHeader(204)
	_, _ = rec.WriteString(`{"error":"oops"}`)

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

func (c *capturingTB) Helper()                           {}
func (c *capturingTB) Errorf(format string, args ...any) { c.errored = true }
func (c *capturingTB) Fatalf(format string, args ...any) { c.errored = true }
func (c *capturingTB) Logf(format string, args ...any)   {}
func (c *capturingTB) Name() string                      { return "capturingTB" }
func (c *capturingTB) Log(args ...any)                   {}
func (c *capturingTB) Error(args ...any)                 { c.errored = true }
func (c *capturingTB) Fatal(args ...any)                 { c.errored = true }

// ---------------------------------------------------------------------------
// ChangePassword contract tests
// ---------------------------------------------------------------------------

func TestHttpAuthUserChangePasswordV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	createContract := contracttest.LoadByID(t, root, "http.auth.user.create.v1")
	c := contracttest.LoadByID(t, root, "http.auth.user.change-password.v1")

	stubIssuer := &stubTokenIssuer{pair: dto.TokenPair{
		AccessToken:  "new-at",
		RefreshToken: "new-rt",
		ExpiresAt:    time.Now().Add(time.Hour),
		SessionID:    "sess-contract-1",
		UserID:       "usr-contract-1",
	}}
	handler, _ := setupContractHandlerWithIssuer(t, stubIssuer)

	// Validate request schema. Passwords must satisfy minLength:8 / maxLength:72 (FMT-25 + bcrypt).
	c.ValidateRequest(t, []byte(`{"oldPassword":"oldP@ss12","newPassword":"newP@ss12"}`))
	c.MustRejectRequest(t, []byte(`{"oldPassword":"oldP@ss12"}`))                                   // missing newPassword
	c.MustRejectRequest(t, []byte(`{"oldPassword":"old12345","newPassword":"new12345","extra":1}`)) // additionalProperties

	// Create a user to change password on.
	userID := createUserForContractTest(t, handler, createContract)

	// Seed bcrypt hash for the user — createUserForContractTest uses testPassword.
	// Since handler already created the user via handleCreate (which bcrypt-hashes),
	// we can directly call the change-password endpoint.
	path := strings.Replace(c.HTTP.Path, "{id}", userID, 1)
	body := `{"oldPassword":"` + testPassword + `","newPassword":"brand-new-P@ss"}`
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	// Verify response schema rejection.
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))

	// Negative cases: required fields missing from response must be rejected.
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{
			"missing sessionId",
			[]byte(`{"data":{"accessToken":"x","refreshToken":"y",` +
				`"expiresAt":"2026-01-01T00:00:00Z","userId":"u","passwordResetRequired":false}}`),
		},
		{
			"missing userId",
			[]byte(`{"data":{"accessToken":"x","refreshToken":"y",` +
				`"expiresAt":"2026-01-01T00:00:00Z","sessionId":"s","passwordResetRequired":false}}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.MustRejectResponse(t, tc.body)
		})
	}
}

func TestHttpAuthUserCreateV1_RequirePasswordResetField(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.user.create.v1")

	// requirePasswordReset is optional — should be accepted (password ≥ 8 chars per FMT-25).
	c.ValidateRequest(t, []byte(`{"username":"u","email":"u@u.com","password":"p1234567","requirePasswordReset":true}`))
	// Without the optional field — also valid.
	c.ValidateRequest(t, []byte(`{"username":"u","email":"u@u.com","password":"p1234567"}`))
}

func TestHttpAuthUserPatchV1_RequirePasswordResetField(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.user.patch.v1")

	// requirePasswordReset as a bool patch field — should be accepted.
	c.ValidateRequest(t, []byte(`{"requirePasswordReset":true}`))
	c.ValidateRequest(t, []byte(`{"requirePasswordReset":false}`))
	c.ValidateRequest(t, []byte(`{"name":"Bob","requirePasswordReset":true}`))
}
