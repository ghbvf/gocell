package configpublish

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func newContractService(t testing.TB) (*Service, *mem.ConfigRepository, *testutil.RecordingWriter) {
	t.Helper()
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	return svc, repo, writer
}

func seedContractEntry(repo *mem.ConfigRepository, value string) {
	const key = "app.name"
	now := time.Now()
	_ = repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
}

// newContractMux registers configpublish routes under the canonical API
// prefix. The TestMux.Route sub-mux structure mirrors production chi so
// auth.Mount strips the prefix off Contract.Path directly; no alias magic.
// RegisterRoutes calls auth.Mount so contract tests exercise the same
// admin policy production uses.
func newContractMux(svc *Service) http.Handler {
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/config", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

// --- HTTP contract test ---

func TestHttpConfigPublishV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.publish.v1")
	svc, repo, _ := newContractService(t)
	seedContractEntry(repo, "value")

	mux := newContractMux(svc)

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
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.rollback.v1")
	svc, repo, _ := newContractService(t)
	seedContractEntry(repo, "value")

	// Publish first to create version 1 so rollback target exists.
	_, err := svc.Publish(auth.TestContext("contract-admin", []string{"admin"}), "app.name")
	require.NoError(t, err)

	mux := newContractMux(svc)

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

// errEnvelope is a minimal struct for asserting the error code in handler responses.
type errEnvelope struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// TestHttpConfigPublishV1_Serve_Unauthorized exercises the real handler for
// 401 (no auth ctx) and 403 (authenticated but lacks admin role) paths, then
// validates the response body shape against the contract's declared error schema
// and asserts the exact error code emitted by the real auth chain.
func TestHttpConfigPublishV1_Serve_Unauthorized(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.publish.v1")
	svc, _, _ := newContractService(t)
	mux := newContractMux(svc)

	path := strings.Replace(c.HTTP.Path, "{key}", "app.name", 1)

	t.Run("401_no_subject", func(t *testing.T) {
		// context.Background() carries no subject → RequireAnyRole → ErrAuthUnauthorized → 401
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.HTTP.Method, path, nil).
			WithContext(context.Background())
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		// Validate envelope shape against contract schema.
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
		// Assert the real error code emitted by RequireAnyRole (missing subject path).
		var env errEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		require.Equal(t, "ERR_AUTH_UNAUTHORIZED", env.Error.Code,
			"missing subject must produce ERR_AUTH_UNAUTHORIZED, not ERR_AUTH_INVALID_TOKEN")
	})

	t.Run("403_insufficient_role", func(t *testing.T) {
		// auth.TestContext with a non-admin role → RequireAnyRole → ErrAuthForbidden → 403
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.HTTP.Method, path, nil).
			WithContext(auth.TestContext("user-readonly", []string{"viewer"}))
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code)
		// Validate envelope shape against contract schema.
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
		// Assert the real error code emitted by RequireAnyRole (insufficient role path).
		var env errEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		require.Equal(t, "ERR_AUTH_FORBIDDEN", env.Error.Code,
			"insufficient role must produce ERR_AUTH_FORBIDDEN, not ERR_AUTH_INVALID_TOKEN")
	})
}

// TestHttpConfigRollbackV1_Serve_Unauthorized mirrors the publish unauthorized test
// for the rollback endpoint, asserting real error codes from the RequireAnyRole chain.
func TestHttpConfigRollbackV1_Serve_Unauthorized(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.rollback.v1")
	svc, _, _ := newContractService(t)
	mux := newContractMux(svc)

	path := strings.Replace(c.HTTP.Path, "{key}", "app.name", 1)

	t.Run("401_no_subject", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"version":1}`)).
			WithContext(context.Background())
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
		var env errEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		require.Equal(t, "ERR_AUTH_UNAUTHORIZED", env.Error.Code,
			"missing subject must produce ERR_AUTH_UNAUTHORIZED")
	})

	t.Run("403_insufficient_role", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"version":1}`)).
			WithContext(auth.TestContext("user-readonly", []string{"viewer"}))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code)
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
		var env errEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		require.Equal(t, "ERR_AUTH_FORBIDDEN", env.Error.Code,
			"insufficient role must produce ERR_AUTH_FORBIDDEN")
	})
}

// --- Event contract tests ---

func TestEventConfigVersionPublishedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.config.version-published.v1")
	svc, repo, writer := newContractService(t)
	seedContractEntry(repo, "value")

	_, err := svc.Publish(auth.TestContext("contract-admin", []string{"admin"}), "app.name")
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1, "Publish must emit one outbox entry")
	entry := writer.Entries[0]
	assert.Equal(t, domain.TopicConfigVersionPublished, entry.EventType)
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name"}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name","configId":"cfg-1","version":1,"sensitive":false}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigRollbackV1Publish_RollbackEmitsStateThenAudit(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	upserted := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")
	rollback := contracttest.LoadByID(t, root, "event.config.rollback.v1")
	svc, repo, writer := newContractService(t)
	seedContractEntry(repo, "v1")

	// Publish first to create a version, then rollback
	_, err := svc.Publish(auth.TestContext("contract-admin", []string{"admin"}), "app.name")
	require.NoError(t, err)
	writer.Entries = nil // reset

	_, err = svc.Rollback(auth.TestContext("contract-admin", []string{"admin"}), "app.name", 1)
	require.NoError(t, err)

	require.Len(t, writer.Entries, 2, "Rollback must emit state-sync then audit outbox entries")

	stateEntry := writer.Entries[0]
	assert.Equal(t, domain.TopicConfigEntryUpserted, stateEntry.EventType)
	upserted.ValidatePayload(t, stateEntry.Payload)
	upserted.ValidateHeaders(t, []byte(`{"event_id":"`+stateEntry.ID+`"}`))
	// Metadata-only: value field must be absent and forbidden.
	upserted.MustRejectPayload(t, []byte(`{"key":"app.name","value":"v1","version":2}`))

	auditEntry := writer.Entries[1]
	assert.Equal(t, domain.TopicConfigRollback, auditEntry.EventType)
	rollback.ValidatePayload(t, auditEntry.Payload)
	rollback.ValidateHeaders(t, []byte(`{"event_id":"`+auditEntry.ID+`"}`))
	rollback.MustRejectPayload(t, []byte(`{"key":"app.name"}`))
	rollback.MustRejectHeaders(t, []byte(`{}`))
}
