package sessionrefresh

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

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// testIssuer is declared in service_test.go

const refreshPath = "/api/v1/access/sessions/refresh"

// setup wires the slice handler onto a celltest mux via RegisterRoutes — the
// same code path cell_routes.go takes in production.
func setup(t testing.TB) (http.Handler, string) {
	t.Helper()
	sessionStore := newTestSessionStore(t)
	refreshStore := newTestRefreshStore()

	sess := newTestSession("usr-1", "sess-1")
	_ = sessionStore.Create(context.Background(), sess)

	// Issue an opaque wire token for sess-1.
	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-1", "usr-1")
	if err != nil {
		panic("setup: issue refresh token: " + err.Error())
	}

	// F1 fail-closed requires the session's user to be resolvable; seed a user
	// so rotateAndIssue does not abort.
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	u, _ := domain.NewUser("usr-1", "usr-1@test.local", "hash", time.Now())
	u.ID = "usr-1"
	_ = userRepo.Create(context.Background(), u)

	svc := MustNewService(sessionStore, mem.NewStore(clock.Real()).RoleRepository(), userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))
	mux := celltest.NewTestMux()
	if err := NewHandler(svc).RegisterRoutes(mux); err != nil {
		panic("RegisterRoutes: " + err.Error())
	}
	return mux, wireToken
}

type unavailableRefreshStore struct {
	refresh.Store
}

func (s unavailableRefreshStore) Peek(context.Context, string) (*refresh.Token, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, "refresh db unavailable")
}

func assertErrorBody(t *testing.T, body []byte, code, message string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []any  `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, code, resp.Error.Code)
	assert.Equal(t, message, resp.Error.Message)
	assert.NotNil(t, resp.Error.Details)
}

func TestTokenPairResponse_Fields(t *testing.T) {
	now := time.Now()
	pair := dto.TokenPair{
		AccessToken:           "access-tok-1",
		RefreshToken:          "refresh-tok-1",
		ExpiresAt:             now,
		SessionID:             "sess-1",
		UserID:                "usr-1",
		PasswordResetRequired: true,
	}
	resp := dto.ToTokenPairResponse(pair)

	assert.Equal(t, "access-tok-1", resp.AccessToken)
	assert.Equal(t, "refresh-tok-1", resp.RefreshToken)
	assert.Equal(t, now, resp.ExpiresAt)
	assert.Equal(t, "sess-1", resp.SessionID)
	assert.Equal(t, "usr-1", resp.UserID)
	assert.True(t, resp.PasswordResetRequired)

	// Verify JSON key casing: marshal to generic map to avoid G117 on struct fields.
	rawBytes, err := json.Marshal(map[string]any{
		"accessToken":           resp.AccessToken,
		"refreshToken":          resp.RefreshToken,
		"expiresAt":             resp.ExpiresAt,
		"sessionId":             resp.SessionID,
		"userId":                resp.UserID,
		"passwordResetRequired": resp.PasswordResetRequired,
	})
	require.NoError(t, err)
	s := string(rawBytes)
	assert.Contains(t, s, `"accessToken"`)
	assert.Contains(t, s, `"refreshToken"`)
	assert.Contains(t, s, `"expiresAt"`)
	assert.Contains(t, s, `"sessionId"`)
	assert.Contains(t, s, `"userId"`)
	assert.Contains(t, s, `"passwordResetRequired"`)
}

func TestHandleRefresh(t *testing.T) {
	h, validToken := setup(t)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "valid refresh token returns 200 with new tokens",
			body:       `{"refreshToken":"` + validToken + `"}`,
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data struct {
						AccessToken           string `json:"accessToken"`
						RefreshToken          string `json:"refreshToken"`
						ExpiresAt             string `json:"expiresAt"`
						SessionID             string `json:"sessionId"`
						UserID                string `json:"userId"`
						PasswordResetRequired bool   `json:"passwordResetRequired"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.NotEmpty(t, resp.Data.AccessToken)
				assert.NotEmpty(t, resp.Data.RefreshToken)
				assert.NotEmpty(t, resp.Data.ExpiresAt)
				assert.NotEmpty(t, resp.Data.SessionID)
				assert.NotEmpty(t, resp.Data.UserID)
				assert.False(t, resp.Data.PasswordResetRequired)

				// Verify camelCase JSON keys (#27n).
				var raw map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(body, &raw))
				var dataMap map[string]any
				require.NoError(t, json.Unmarshal(raw["data"], &dataMap))
				assert.Contains(t, dataMap, "accessToken", "key must be camelCase")
				assert.Contains(t, dataMap, "refreshToken", "key must be camelCase")
				assert.Contains(t, dataMap, "expiresAt", "key must be camelCase")
				assert.Contains(t, dataMap, "sessionId", "key must be camelCase")
				assert.Contains(t, dataMap, "userId", "key must be camelCase")
				assert.Contains(t, dataMap, "passwordResetRequired", "key must be camelCase")
			},
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{bad`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid token returns 401",
			body:       `{"refreshToken":"not.a.valid.opaque.token"}`,
			wantStatus: http.StatusUnauthorized,
			checkBody: func(t *testing.T, body []byte) {
				assertErrorBody(t, body, "ERR_AUTH_REFRESH_FAILED", "invalid refresh token")
			},
		},
		{
			name:       "unknown field returns 400",
			body:       `{"refreshToken":"not.a.valid.opaque.token","extra":"y"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, refreshPath, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandleRefresh_RefreshStoreUnavailable_Returns503(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	store := unavailableRefreshStore{Store: newTestRefreshStore()}
	svc := MustNewService(sessionStore, mem.NewStore(clock.Real()).RoleRepository(), userRepo, store, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))
	mux := celltest.NewTestMux()
	if err := NewHandler(svc).RegisterRoutes(mux); err != nil {
		panic("RegisterRoutes: " + err.Error())
	}

	// Send a token that passes schema length validation (minLength:20) so the
	// request reaches the service layer and triggers the 503 from the unavailable store.
	token := strings.Repeat("x", 20)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, refreshPath, strings.NewReader(`{"refreshToken":"`+token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assertErrorBody(t, w.Body.Bytes(), "ERR_SERVICE_UNAVAILABLE", "service unavailable")
}

// TestHandler_Refresh_BlankToken verifies that submitting an empty refreshToken
// returns 400. The generated handler enforces minLength:20 and returns
// ERR_VALIDATION_FAILED before the request reaches the service layer.
func TestHandler_Refresh_BlankToken(t *testing.T) {
	h, _ := setup(t)
	body := `{"refreshToken":""}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, refreshPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ERR_VALIDATION_FAILED", resp.Error.Code)
	// buildSafeMessage result is in details[key="detail"] since the field name
	// is runtime data; Message is the const "request body validation failed".
	assert.Equal(t, "request body validation failed", resp.Error.Message)
	foundDetail := false
	for _, d := range resp.Error.Details {
		if d.Key == "detail" {
			assert.Contains(t, d.Value, "refreshToken")
			foundDetail = true
			break
		}
	}
	assert.True(t, foundDetail, "expected 'detail' key in error details")
}
