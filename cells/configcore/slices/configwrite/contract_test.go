package configwrite

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newContractService(t testing.TB) (*Service, *mem.ConfigRepository, *testutil.RecordingWriter) {
	t.Helper()
	repo := mem.NewConfigRepository()
	writer := &testutil.RecordingWriter{}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))
	return svc, repo, writer
}

// newContractMux registers configwrite routes under the canonical API
// prefix. TestMux.Route mirrors production chi so auth.Mount strips the
// prefix off Contract.Path directly; no alias magic. RegisterRoutes
// calls auth.Mount to install the admin policy, so the contract test
// exercises the same guard the production mux uses — not just the
// happy-path handler.
func newContractMux(svc *Service) http.Handler {
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/config", func(sub cell.RouteMux) {
		h.RegisterRoutes(sub)
	})
	return mux
}

// --- HTTP contract test ---

func TestHttpConfigWriteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.write.v1")
	svc, _, _ := newContractService(t)

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
	svc, _, _ := newContractService(t)
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
	svc, _, writer := newContractService(t)

	_, err := svc.Create(context.Background(), CreateInput{Key: "app.name", Value: "myapp"})
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1, "Create must emit one outbox entry")
	entry := writer.Entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"action":"created","key":"app.name"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigChangedV1Publish_Update(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService(t)

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)
	writer.Entries = nil // reset

	_, err = svc.Update(context.Background(), UpdateInput{Key: "k", Value: "v2"})
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1, "Update must emit one outbox entry")
	entry := writer.Entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
}

func TestEventConfigChangedV1Publish_Delete(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService(t)

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	writer.Entries = nil // reset

	err = svc.Delete(context.Background(), "k")
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1, "Delete must emit one outbox entry")
	entry := writer.Entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
}
