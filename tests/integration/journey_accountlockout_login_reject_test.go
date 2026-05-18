//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// TestJAccountlockoutLoginReject implements journeys/J-accountlockout.yaml
// passCriteria "锁定期间登录被拒绝" — checkRef
// journey.J-accountlockout.login-reject. The verify runner resolves that
// ref to ^TestJAccountlockoutLoginReject$ via verify.kebabToCamelCase and
// executes it under -tags=integration via `gocell verify journey
// --id=J-accountlockout`.
//
// J-accountlockout is lifecycle: experimental, so governance VERIFY-06 does
// NOT force-execute this test in `gocell validate --strict`
// (kernel/governance/rules_verify.go:353 returns nil for non-active
// journeys). It is kept Docker-free regardless so `gocell verify journey`
// runs it without a container runtime, consistent with
// TestJSsologinSessionDb / TestJSsologinErrorPathsUniform in this package.
//
// The criterion asserts a locked account that attempts login receives the
// SAME opaque 401 envelope as missing-user / wrong-password (FU-1 #513
// anti-enumeration contract, contracts/http/auth/login/v1/contract.yaml:24):
// a client must not be able to discover "this account exists but is locked".
// In sessionlogin.loginInTx the locked-account path is the pre-bcrypt
// credentialauthority baseline failure
// (cells/accesscore/slices/sessionlogin/service.go:275): KindUnauthenticated
// + ErrAuthLoginFailed + "invalid credentials" with the real (locked) reason
// confined to WithInternal (server-side slog only, never on wire).
//
// Layer compromise: the wire envelope is governed solely by pkg/errcode +
// pkg/httputil, so asserting at that seam IS asserting the wire-shape
// contract. The full programmatic proof — a real locked user driven through
// the lock endpoint then login over an HTTP + PG roundtrip — is owned by
// T4's tests/integration/l2atomicity/TestL2_LoginUniform401
// (inactive_account_locked subcase, testcontainers e2e); see
// tests/integration/l2atomicity/doc.go:55-57 and plan
// docs/plans/202605082145-034-pg-corecell-b-route-plan.md §T4 ↔ FU-4 for
// the T4 (programmatic) ↔ FU-4 (declarative journey spec) division of
// labor. This declarative test must not duplicate T4's PG e2e.
//
// errMsgInvalidCredentials ("invalid credentials") is unexported in the
// sessionlogin package; the literal here is the wire contract value pinned
// in contracts/http/auth/login/v1/contract.yaml:24.
func TestJAccountlockoutLoginReject(t *testing.T) {
	t.Parallel()

	const wantMessage = "invalid credentials"

	// Locked-account login attempt — mirrors service.go:275 (pre-bcrypt
	// credentialauthority baseline fail). The real "status=locked" reason is
	// confined to WithInternal and must not surface on the wire.
	lockedErr := errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
		wantMessage,
		errcode.WithInternal(fmt.Sprintf(
			"credentialauthority: pre-bcrypt baseline fail (user_id=%s status=%v)",
			"u-locked-acct", "locked")))

	rec := httptest.NewRecorder()
	httputil.WriteError(context.Background(), rec, lockedErr)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	assert.Equal(t, http.StatusUnauthorized, res.StatusCode,
		"locked-account login must be rejected with HTTP 401")
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"),
		"canonical error envelope is JSON")

	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env),
		"body must be the canonical error envelope")
	assert.Equal(t, "ERR_AUTH_LOGIN_FAILED", env.Error.Code)
	assert.Equal(t, wantMessage, env.Error.Message)
	assert.Empty(t, env.Error.Details,
		"locked status must not leak to the client (WithInternal only)")
}
