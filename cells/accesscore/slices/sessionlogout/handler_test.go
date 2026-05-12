package sessionlogout

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

const (
	invalidUUID    = "not-a-uuid-string"
	logoutBasePath = "/api/v1/access/sessions/"
)

func newHandlerLogoutRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

// setup wires the slice handler onto a celltest mux via RegisterRoutes — the
// same code path cell_routes.go takes in production.
func setup(t testing.TB) http.Handler {
	t.Helper()
	sessionRepo := testutil.RealSessionRepo(t)
	sess, _ := domain.NewSession(testutil.TestID("usr-1"), "access-tok", time.Now().Add(time.Hour), time.Now())
	sess.ID = testutil.TestID("sess-1")
	_ = sessionRepo.Create(context.Background(), sess)
	// Victim session owned by a different user — used to prove IDOR guard.
	other, _ := domain.NewSession(testutil.TestID("usr-victim"), "at-v", time.Now().Add(time.Hour), time.Now())
	other.ID = testutil.TestID("sess-victim")
	_ = sessionRepo.Create(context.Background(), other)

	svc := MustNewService(sessionRepo, newHandlerLogoutRefreshStore(), slog.Default(), WithTxManager(persistence.WrapForCell(noopTxRunner{})))
	mux := celltest.NewTestMux()
	if err := NewHandler(svc).RegisterRoutes(mux); err != nil {
		panic("RegisterRoutes: " + err.Error())
	}
	return mux
}

func TestHandleLogout(t *testing.T) {
	tests := []struct {
		name       string
		sessionID  string
		caller     string // subject injected into ctx; empty = no auth ctx
		wantStatus int
	}{
		{
			name:       "own session returns 204",
			sessionID:  testutil.TestID("sess-1"),
			caller:     testutil.TestID("usr-1"),
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "nonexistent session returns 404",
			sessionID:  testutil.TestID("no-such-sess"),
			caller:     testutil.TestID("usr-1"),
			wantStatus: http.StatusNotFound,
		},
		{
			// IDOR: another user's existing session id must look identical
			// to a non-existent id — 404, not 403 — so attackers cannot
			// enumerate session ownership.
			name:       "other user's session returns 404 not 403",
			sessionID:  testutil.TestID("sess-victim"),
			caller:     testutil.TestID("usr-attacker"),
			wantStatus: http.StatusNotFound,
		},
		{
			// Defense-in-depth: if the route is accidentally declared public
			// (no auth middleware injected subject), the handler must fail
			// closed rather than allow anonymous revokes.
			name:       "missing subject in ctx returns 401",
			sessionID:  testutil.TestID("sess-1"),
			caller:     "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			// Defense-in-depth: Principal is present in context (ok=true) but
			// Subject is empty — handler must still reject with 401.
			name:       "principal present but empty subject returns 401",
			sessionID:  testutil.TestID("sess-1"),
			caller:     "empty-subject-sentinel",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid UUID in path returns 400",
			sessionID:  invalidUUID,
			caller:     testutil.TestID("usr-1"),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			w := httptest.NewRecorder()
			var ctx context.Context
			switch {
			case tc.name == "principal present but empty subject returns 401":
				// Inject a Principal with ok=true but Subject="" to exercise the
				// second branch of: if !ok || p.Subject == ""
				ctx = auth.WithPrincipal(context.Background(), &auth.Principal{
					Kind:       auth.PrincipalUser,
					Subject:    "",
					AuthMethod: "test",
				})
			case tc.caller != "":
				ctx = auth.TestContext(tc.caller, nil)
			default:
				ctx = context.Background()
			}
			req := httptest.NewRequest(http.MethodDelete, logoutBasePath+tc.sessionID, nil).WithContext(ctx)
			h.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)

			if tc.name == "invalid UUID in path returns 400" {
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, string(errcode.ErrValidationInvalidUUID), body.Error.Code)
			}
		})
	}
}

// TestHandler_Logout_BlankID tests the service directly with a blank sessionID
// because the router pattern "/{id}" requires a non-empty path segment, making
// an empty id unreachable via a real HTTP request. The service-level test
// ensures the validation message uses the contract field name "id".
func TestHandler_Logout_BlankID(t *testing.T) {
	svc, repo := newTestService(t)
	seedSession(repo, "sess-1", "usr-1")

	err := svc.Logout(context.Background(), "", "usr-1")
	require.Error(t, err)

	var coded *errcode.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, errcode.ErrAuthLogoutInvalidInput, coded.Code)
	assert.Equal(t, "validation: required field missing", coded.Message)
	var gotField string
	for _, attr := range coded.Details {
		if attr.Key == "field" {
			gotField = attr.Value.String()
			break
		}
	}
	assert.Equal(t, "id", gotField, "details must carry the field name")
}
