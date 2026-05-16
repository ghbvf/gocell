//go:build integration

package l2_atomicity

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestL2_ChangePasswordOldPasswordIncorrect validates the FU-2 (#512) errcode
// closure: ChangePassword distinguishes "wrong old password" from generic login
// failure via a dedicated errcode, surfaced as ERR_AUTH_OLD_PASSWORD_INCORRECT.
//
// Success path also drives the S4b credential cascade: a successful password
// change atomically bumps users.authz_epoch and revokes prior sessions, so the
// previously-issued access JWT must be rejected by any JWT-guarded endpoint.
//
// Source of truth:
//   - pkg/errcode/errcode.go::ErrAuthOldPasswordIncorrect
//   - contracts/http/auth/user/change-password/v1/contract.yaml 401 description
//   - cells/accesscore/slices/identitymanage/service.go::changePasswordInTx
func TestL2_ChangePasswordOldPasswordIncorrect(t *testing.T) {
	h := newL2Harness(t)

	const victimUsername = "l2-pwdchange-user"
	const victimPassword = "VictimPass!99"
	const newPassword = "NewVictimPass!00"

	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, "pwdchange@l2.local", victimPassword)

	victimLogin := httpLogin(t, h.base, victimUsername, victimPassword)
	epochBefore := queryUserAuthzEpoch(t, h, victimID)

	t.Run("wrong_old_password_returns_401_with_dedicated_errcode", func(t *testing.T) {
		env := httpChangePasswordExpect(t, h.base, victimLogin.AccessToken, victimID,
			"WrongOldPassword!00", newPassword, http.StatusUnauthorized)
		assert.Equal(t, "ERR_AUTH_OLD_PASSWORD_INCORRECT", env.Error.Code,
			"wrong-old-password must surface as ERR_AUTH_OLD_PASSWORD_INCORRECT (FU-2 errcode split)")

		// Epoch must not advance — change-password tx was aborted before the
		// credentialinvalidate funnel.
		epochAfter := queryUserAuthzEpoch(t, h, victimID)
		assert.Equal(t, epochBefore, epochAfter,
			"users.authz_epoch must be unchanged after rejected change-password (epochBefore=%d, epochAfter=%d)",
			epochBefore, epochAfter)
	})

	t.Run("correct_old_password_succeeds_and_bumps_epoch_and_invalidates_old_token", func(t *testing.T) {
		// Re-fetch login because previous sub-test did not consume sessions.
		fresh := httpLogin(t, h.base, victimUsername, victimPassword)
		epochBefore := queryUserAuthzEpoch(t, h, victimID)

		res := httpChangePasswordOK(t, h.base, fresh.AccessToken, victimID, victimPassword, newPassword)
		require.NotEmpty(t, res.AccessToken, "success must include fresh accessToken")
		require.NotEmpty(t, res.RefreshToken, "success must include fresh refreshToken")
		assert.Equal(t, victimID, res.UserID)

		epochAfter := queryUserAuthzEpoch(t, h, victimID)
		assert.Greater(t, epochAfter, epochBefore,
			"users.authz_epoch must advance after successful change-password (epochBefore=%d, epochAfter=%d)",
			epochBefore, epochAfter)

		// Old access token must be rejected by any JWT-guarded endpoint.
		// AuthMiddleware collapses every JWT verification failure (signature /
		// expiry / intent / epoch / session-state) to ERR_AUTH_UNAUTHORIZED to
		// prevent token-state enumeration.
		stale := httpGetUserExpectError(t, h.base, fresh.AccessToken, victimID, http.StatusUnauthorized)
		assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", stale.Error.Code,
			"stale access token must be rejected with the generic ERR_AUTH_UNAUTHORIZED (enumeration defense)")

		// The freshly-returned token must work.
		httpGetUser(t, h.base, res.AccessToken, victimID)
	})
}
