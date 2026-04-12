package identitymanage

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func setupContractHandler() http.Handler {
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default())
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Handle("POST /api/v1/auth/users", http.HandlerFunc(h.handleCreate))
	mux.Handle("DELETE /api/v1/auth/users/{id}", http.HandlerFunc(h.handleDelete))
	return mux
}

func createUserForContractTest(t *testing.T, handler http.Handler, contract *contracttest.Contract) string {
	t.Helper()
	body := `{"username":"alice","email":"a@b.com","password":"secret123"}`
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

	c.ValidateRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"secret123"}`))
	c.MustRejectRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"s","extra":"bad"}`))

	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"username":"alice","email":"a@b.com","password":"secret123"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	c.ValidateHTTPResponseRecorder(t, recorder)
}

func TestHttpAuthUserGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.get.v1")

	c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice","email":"a@b.com","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUpdateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.update.v1")

	c.ValidateRequest(t, []byte(`{"email":"new@b.com"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice","email":"new@b.com","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectRequest(t, []byte(`{"email":"a","extra":"bad"}`))
}

func TestHttpAuthUserPatchV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.patch.v1")

	c.ValidateRequest(t, []byte(`{"name":"Bob"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice","email":"a@b.com","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
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
	c := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")

	c.ValidateResponse(t, []byte(`{"data":{"status":"locked"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUnlockV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.unlock.v1")

	c.ValidateResponse(t, []byte(`{"data":{"status":"active"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestEventUserCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.created.v1")

	// Schema validation: payload requires user_id + username, headers requires event_id.
	c.ValidatePayload(t, []byte(`{"user_id":"usr-1","username":"alice"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-123"}`))
	c.MustRejectPayload(t, []byte(`{"user_id":"x"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventUserLockedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.locked.v1")

	// Schema validation: payload requires user_id, headers requires event_id.
	c.ValidatePayload(t, []byte(`{"user_id":"usr-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-456"}`))
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
