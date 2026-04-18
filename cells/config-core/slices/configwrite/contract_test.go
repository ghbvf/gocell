package configwrite

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

func newContractService() (*Service, *mem.ConfigRepository, *recordingWriter) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	return svc, repo, writer
}

// newContractMux registers configwrite routes on a mux at the canonical API
// prefix using auth.Secured (via RegisterRoutes + http.StripPrefix). This
// mirrors the production wiring so that contract tests exercise the full
// auth-guard path, not just the happy-path handler.
func newContractMux(svc *Service) *http.ServeMux {
	h := NewHandler(svc)
	sub := http.NewServeMux()
	h.RegisterRoutes(sub)
	outer := http.NewServeMux()
	outer.Handle("/api/v1/config/", http.StripPrefix("/api/v1/config", sub))
	return outer
}

// --- HTTP contract test ---

func TestHttpConfigWriteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.write.v1")
	svc, _, _ := newContractService()

	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"key":"app.name","value":"myapp","sensitive":false}`))
	c.MustRejectRequest(t, []byte(`{"key":"k","value":"v","extra":"bad"}`))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"key":"app.name","value":"myapp"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{dto.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"id":"x"}}`))
}

// TestHttpConfigWriteV1_AuthzNegative validates the 401/403 failure semantics
// that are part of the http.config.write.v1 interface contract. These paths
// are tested here alongside the happy-path contract test so that auth-guard
// regressions are caught at the contract boundary, not just in unit tests.
func TestHttpConfigWriteV1_AuthzNegative(t *testing.T) {
	svc, _, _ := newContractService()
	mux := newContractMux(svc)
	body := `{"key":"app.name","value":"myapp"}`

	cases := []struct {
		name        string
		injectAuth  bool
		subject     string
		roles       []string
		wantStatus  int
		wantErrCode string
	}{
		{"no_auth", false, "", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", true, "user-1", []string{"viewer"}, http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/config/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.injectAuth {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			mux.ServeHTTP(rec, req)
			assert.Equal(t, tc.wantStatus, rec.Code)
			var resp struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tc.wantErrCode, resp.Error.Code)
		})
	}
}

// --- Event contract tests ---

func TestEventConfigChangedV1Publish_Create(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService()

	_, err := svc.Create(context.Background(), CreateInput{Key: "app.name", Value: "myapp"})
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Create must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"action":"created","key":"app.name"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigChangedV1Publish_Update(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService()

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)
	writer.entries = nil // reset

	_, err = svc.Update(context.Background(), UpdateInput{Key: "k", Value: "v2"})
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Update must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
}

func TestEventConfigChangedV1Publish_Delete(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService()

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	writer.entries = nil // reset

	err = svc.Delete(context.Background(), "k")
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Delete must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
}
