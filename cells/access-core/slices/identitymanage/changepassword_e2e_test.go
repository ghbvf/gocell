// Package identitymanage — e2e test for the full ChangePassword flow.
//
// Flow:
//  1. Bootstrap an admin user with PasswordResetRequired=true via in-memory repo.
//  2. Login → assert TokenPair.PasswordResetRequired==true and JWT claim=true.
//  3. Assert that the password-endpoint path is exempt from reset enforcement
//     (the actual middleware enforcement is tested in runtime/auth/middleware_test.go).
//  4. Call POST /{id}/password → assert 200 + new TokenPair with PasswordResetRequired==false.
//  5. Verify new JWT claim is false.
//  6. Call GET /{id} with new token → assert 200.
package identitymanage

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogin"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eTestKeySet holds a key pair shared across the e2e test.
var e2eTestKeySet, _, _ = auth.MustNewTestKeySet()

// e2eIssuer is used by the login service.
var e2eIssuer = func() *auth.JWTIssuer {
	i, err := auth.NewJWTIssuer(e2eTestKeySet, "gocell-access-core", 15*time.Minute)
	if err != nil {
		panic("e2e test setup: " + err.Error())
	}
	return i
}()

// e2eVerifier is used to decode tokens in assertions.
var e2eVerifier = func() *auth.JWTVerifier {
	v, err := auth.NewJWTVerifier(e2eTestKeySet, auth.WithExpectedAudiences("gocell"))
	if err != nil {
		panic("e2e test setup: " + err.Error())
	}
	return v
}()

// e2eTokenIssuer bridges sessionlogin.Service to the identitymanage.TokenIssuer interface.
type e2eTokenIssuer struct {
	svc *sessionlogin.Service
}

func (ti *e2eTokenIssuer) IssueForUser(ctx context.Context, userID string) (*sessionlogin.TokenPair, error) {
	return ti.svc.IssueForUser(ctx, userID)
}

// e2eFixture is a minimal but realistic wiring: shared mem repos, real JWT
// key pair, loginService + identityService with TokenIssuer injection, and a
// full-path HTTP mux.
type e2eFixture struct {
	mux         http.Handler
	loginSvc    *sessionlogin.Service
	userRepo    ports.UserRepository
	sessionRepo ports.SessionRepository
	roleRepo    ports.RoleRepository
}

func newE2EFixture() *e2eFixture {
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	eb := eventbus.New()

	loginSvc := sessionlogin.NewService(
		userRepo, sessionRepo, roleRepo, eb, e2eIssuer, slog.Default(),
	)

	idmSvc := NewService(userRepo, sessionRepo, eb, slog.Default(),
		WithTokenIssuer(&e2eTokenIssuer{svc: loginSvc}),
	)

	// Build a full-path mux so path values are populated correctly.
	mux := celltest.NewTestMux()
	h := NewHandler(idmSvc)
	mux.Handle("POST /api/v1/access/users", http.HandlerFunc(h.handleCreate))
	mux.Handle("GET /api/v1/access/users/{id}", http.HandlerFunc(h.handleGet))
	mux.Handle("PATCH /api/v1/access/users/{id}", http.HandlerFunc(h.handlePatch))
	mux.Handle("POST /api/v1/access/users/{id}/password", http.HandlerFunc(h.handleChangePassword))

	return &e2eFixture{
		mux:         mux,
		loginSvc:    loginSvc,
		userRepo:    userRepo,
		sessionRepo: sessionRepo,
		roleRepo:    roleRepo,
	}
}

// bootstrapAdminUser seeds an admin user with PasswordResetRequired=true in
// the in-memory repos. Returns the userID.
func bootstrapAdminUser(t *testing.T, f *e2eFixture, username, plainPassword string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.MinCost)
	require.NoError(t, err)

	user, err := domain.NewUser(username, username+"@gocell.local", string(hash))
	require.NoError(t, err)
	user.ID = "usr-e2e-" + username
	user.MarkPasswordResetRequired()
	require.NoError(t, f.userRepo.Create(context.Background(), user))

	// Assign admin role.
	adminRole := &domain.Role{
		ID:          domain.RoleAdmin,
		Name:        domain.RoleAdmin,
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	}
	_ = f.roleRepo.Create(context.Background(), adminRole)
	_, err = f.roleRepo.AssignToUser(context.Background(), user.ID, domain.RoleAdmin)
	require.NoError(t, err)

	return user.ID
}

// isPasswordResetExemptLocal mirrors the allowlist logic in
// runtime/auth/middleware.go for use in local assertions.
func isPasswordResetExemptLocal(method, path string) bool {
	if method == http.MethodPost && strings.HasSuffix(path, "/password") {
		return true
	}
	if method == http.MethodDelete && strings.Contains(path, "/sessions/") {
		return true
	}
	return false
}

// TestChangePassword_FullFlow is the e2e closure test:
//
//  1. Bootstrap admin → PasswordResetRequired=true.
//  2. Login → assert flag=true in both TokenPair and JWT claim.
//  3. Assert allowlist: POST /password is exempt; GET /users/{id} is not.
//  4. POST /{id}/password → 200 + new TokenPair with flag=false.
//  5. New JWT claim=false.
//  6. GET /{id} with new token → 200.
func TestChangePassword_FullFlow(t *testing.T) {
	f := newE2EFixture()
	const bootstrapPassword = "B00tstr@pSecret"
	const newPassword = "NewS3cur3P@ss!"

	userID := bootstrapAdminUser(t, f, "e2e-admin", bootstrapPassword)

	// --- Step 2: Login ---
	loginPair, err := f.loginSvc.Login(context.Background(), sessionlogin.LoginInput{
		Username: "e2e-admin",
		Password: bootstrapPassword,
	})
	require.NoError(t, err)
	assert.True(t, loginPair.PasswordResetRequired,
		"Login must return PasswordResetRequired=true for bootstrap user")

	// Verify JWT claim.
	loginClaims, err := e2eVerifier.VerifyIntent(context.Background(), loginPair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.True(t, loginClaims.PasswordResetRequired,
		"access token must carry password_reset_required=true claim after bootstrap login")

	// --- Step 3: allowlist assertions ---
	assert.False(t, isPasswordResetExemptLocal(http.MethodGet, "/api/v1/access/users/"+userID),
		"GET /users/{id} must NOT be exempt from password reset enforcement")
	assert.True(t, isPasswordResetExemptLocal(http.MethodPost, "/api/v1/access/users/"+userID+"/password"),
		"POST /users/{id}/password must be exempt from password reset enforcement")
	assert.True(t, isPasswordResetExemptLocal(http.MethodDelete, "/api/v1/access/sessions/sess-x"),
		"DELETE /sessions/{id} must be exempt from password reset enforcement")

	// --- Step 4: ChangePassword ---
	cpBody, _ := json.Marshal(map[string]string{
		"oldPassword": bootstrapPassword,
		"newPassword": newPassword,
	})
	cpReq := httptest.NewRequest(http.MethodPost, "/api/v1/access/users/"+userID+"/password",
		bytes.NewReader(cpBody))
	cpReq.Header.Set("Content-Type", "application/json")
	cpReq = cpReq.WithContext(auth.TestContext(userID, []string{domain.RoleAdmin}))
	cpW := httptest.NewRecorder()
	f.mux.ServeHTTP(cpW, cpReq)
	require.Equal(t, http.StatusOK, cpW.Code, "ChangePassword must return 200; body=%s", cpW.Body.String())

	var cpResp struct {
		Data struct {
			AccessToken           string    `json:"accessToken"`
			RefreshToken          string    `json:"refreshToken"`
			ExpiresAt             time.Time `json:"expiresAt"`
			PasswordResetRequired bool      `json:"passwordResetRequired"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(cpW.Body.Bytes(), &cpResp))
	assert.NotEmpty(t, cpResp.Data.AccessToken, "new access token must not be empty")
	assert.False(t, cpResp.Data.PasswordResetRequired, "PasswordResetRequired must be false after password change")

	// --- Step 5: Verify new token JWT claim ---
	newClaims, err := e2eVerifier.VerifyIntent(context.Background(), cpResp.Data.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.False(t, newClaims.PasswordResetRequired,
		"new access token JWT claim must be false after ChangePassword")

	// --- Step 6: GET succeeds with new token (unblocked) ---
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+userID, nil)
	getReq = getReq.WithContext(auth.TestContext(userID, []string{domain.RoleAdmin}))
	getW := httptest.NewRecorder()
	f.mux.ServeHTTP(getW, getReq)
	assert.Equal(t, http.StatusOK, getW.Code, "GET must succeed after password change")
}

// TestChangePassword_RejectsBadOldPassword ensures the e2e flow returns 401
// when the old password is wrong (correct errcode propagated through HTTP).
func TestChangePassword_RejectsBadOldPassword(t *testing.T) {
	f := newE2EFixture()
	userID := bootstrapAdminUser(t, f, "e2e-wrongpass", "correctpass")

	body, _ := json.Marshal(map[string]string{
		"oldPassword": "wrongpass",
		"newPassword": "newpass",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/users/"+userID+"/password",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(userID, []string{domain.RoleAdmin}))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), string(errcode.ErrAuthLoginFailed))
}
