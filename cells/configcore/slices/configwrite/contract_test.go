package configwrite

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	configdelete "github.com/ghbvf/gocell/generated/contracts/http/config/delete/v1"
	update "github.com/ghbvf/gocell/generated/contracts/http/config/update/v1"
	write "github.com/ghbvf/gocell/generated/contracts/http/config/write/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func newContractService(t testing.TB) (*Service, *testutil.RecordingWriter) {
	t.Helper()
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)
	return svc, writer
}

// newContractMux registers all configwrite routes under the canonical API prefix.
func newContractMux(svc *Service) http.Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	writeH := write.NewHandler(WriteAdapter{svc}, policy)
	updateH := update.NewHandler(UpdateAdapter{svc}, policy)
	deleteH := configdelete.NewHandler(DeleteAdapter{svc}, policy)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/config", func(sub cell.RouteMux) {
		if err := writeH.RegisterRoutes(sub); err != nil {
			panic("write.RegisterRoutes: " + err.Error())
		}
		if err := updateH.RegisterRoutes(sub); err != nil {
			panic("update.RegisterRoutes: " + err.Error())
		}
		if err := deleteH.RegisterRoutes(sub); err != nil {
			panic("delete.RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

// --- HTTP contract test ---

func TestHttpConfigWriteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.write.v1")
	svc, _ := newContractService(t)

	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"key":"app.name","value":"myapp","sensitive":false}`))
	c.MustRejectRequest(t, []byte(`{"key":"k","value":"v","extra":"bad"}`))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"key":"app.name","value":"myapp"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"id":"x"}}`))
}

// TestHttpConfigWriteV1_AuthzNegative validates the 401/403 failure semantics
// that are part of the http.config.write.v1 interface contract.
func TestHttpConfigWriteV1_AuthzNegative(t *testing.T) {
	svc, _ := newContractService(t)
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

func TestEventConfigEntryUpsertedV1Publish_Create(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")
	svc, writer := newContractService(t)

	_, err := svc.Create(auth.TestContext("contract-admin", []string{"admin"}), CreateInput{Key: "app.name", Value: "myapp"})
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1, "Create must emit one outbox entry")
	entry := writer.Entries[0]
	assert.Equal(t, domain.TopicConfigEntryUpserted, entry.EventType)
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	// Metadata-only: missing version must be rejected; value field must be rejected.
	c.MustRejectPayload(t, []byte(`{"key":"app.name"}`))
	c.MustRejectPayload(t, []byte(`{"version":1}`))
	c.MustRejectPayload(t, []byte(`{"key":"","version":1}`))
	c.MustRejectPayload(t, []byte(`{"key":"   ","version":1}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":0}`))
	// value field is now forbidden (additionalProperties: false)
	c.MustRejectPayload(t, []byte(`{"key":"app.name","value":"myapp","version":1}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigEntryUpsertedV1Publish_Update(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")
	svc, writer := newContractService(t)

	_, err := svc.Create(auth.TestContext("contract-admin", []string{"admin"}), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)
	writer.Entries = nil // reset

	_, err = svc.Update(auth.TestContext("contract-admin", []string{"admin"}), UpdateInput{Key: "k", Value: "v2", ExpectedVersion: 1})
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1, "Update must emit one outbox entry")
	entry := writer.Entries[0]
	assert.Equal(t, domain.TopicConfigEntryUpserted, entry.EventType)
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
}

func TestEventConfigEntryDeletedV1Publish_Delete(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.config.entry-deleted.v1")
	svc, writer := newContractService(t)

	_, err := svc.Create(auth.TestContext("contract-admin", []string{"admin"}), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	writer.Entries = nil // reset

	err = svc.Delete(auth.TestContext("contract-admin", []string{"admin"}), "k", 1)
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1, "Delete must emit one outbox entry")
	entry := writer.Entries[0]
	assert.Equal(t, domain.TopicConfigEntryDeleted, entry.EventType)
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))

	// Negative cases: missing required fields or invalid version.
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectPayload(t, []byte(`{"key":""}`))
	c.MustRejectPayload(t, []byte(`{"key":"   "}`))
	c.MustRejectPayload(t, []byte(`{"key":"k"}`))                           // missing version
	c.MustRejectPayload(t, []byte(`{"key":"k","version":0}`))               // invalid version
	c.MustRejectPayload(t, []byte(`{"key":"k","version":1,"value":"old"}`)) // extra field
}
