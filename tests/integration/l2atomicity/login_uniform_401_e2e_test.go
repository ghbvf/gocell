//go:build integration

package l2atomicity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestL2_LoginUniform401 verifies the FU-1 (#513) wire-shape contract:
// missing user / wrong password / inactive account all return 401 with the
// same envelope: code=ERR_AUTH_LOGIN_FAILED, message="invalid credentials".
// Account-enumeration defense: clients cannot distinguish the three branches.
//
// Source of truth:
//   - contracts/http/auth/login/v1/contract.yaml 401 description
//   - cells/accesscore/slices/sessionlogin/service.go::errMsgInvalidCredentials
//   - pkg/errcode/errcode.go::ErrAuthLoginFailed
func TestL2_LoginUniform401(t *testing.T) {
	h := newL2Harness(t)

	// Pre-seed a non-admin victim and lock it for the inactive_account sub-case.
	// Direct UPDATE users SET status='locked' on the sole admin trips the
	// effective_admin_invariant DB trigger; a non-admin victim avoids that.
	const victimUsername = "l2-victim"
	const victimPassword = "VictimPass!99"
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, "victim@l2.local", victimPassword)
	httpLockUser(t, h.base, adminLogin.AccessToken, victimID)

	cases := []struct {
		name     string
		username string
		password string
	}{
		{name: "missing_user", username: "no-such-user", password: "AnyPass!00"},
		{name: "wrong_password_existing_user", username: victimUsername, password: "WrongPassword!00"},
		{name: "inactive_account_locked", username: victimUsername, password: victimPassword},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := httpLoginExpect401(t, h.base, tc.username, tc.password)
			assert.Equal(t, "ERR_AUTH_LOGIN_FAILED", env.Error.Code,
				"%s: wire code must be uniform", tc.name)
			assert.Equal(t, "invalid credentials", env.Error.Message,
				"%s: wire message must be uniform", tc.name)
		})
	}
}

// TestL2_HarnessSeedAdminLoginWorks is the smoke baseline that every other test
// implicitly depends on: the harness's setup/admin seed must be usable for
// login. Diagnostic value if any harness wiring drifts.
func TestL2_HarnessSeedAdminLoginWorks(t *testing.T) {
	h := newL2Harness(t)
	res := httpLogin(t, h.base, adminUsername, adminPassword)
	assert.NotEmpty(t, res.AccessToken)
	assert.NotEmpty(t, res.RefreshToken)
	assert.NotEmpty(t, res.SessionID)
}
