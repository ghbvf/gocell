//go:build integration

package l2atomicity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestL2_LoginUniform401 verifies the FU-1 (#513) wire-shape contract:
// missing user / wrong password / inactive account all return 401 with the
// same envelope. Account-enumeration defense: clients cannot distinguish
// the three branches at the wire level.
//
// Assertion strength: this test enforces complete envelope equality across
// the three error paths (modulo per-request request_id), not just
// code+message field equality. The prior version asserted only code +
// message, which would have let an unknown-field regression (e.g. an
// internal reason leaking into details, or a new top-level diagnostic
// field) silently differ between branches.
//
// Source of truth:
//   - contracts/http/auth/login/v1/contract.yaml 401 description
//   - contracts/shared/errors/error-response-v1.schema.json
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

	normalized := make(map[string][]byte, len(cases))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := httpLoginExpect401Raw(t, h.base, tc.username, tc.password)
			body := normalizeErrorEnvelope(t, raw)

			// Field-level checks (kept alongside the byte-equal check below
			// so individual regressions are easier to read).
			env := errorEnvelope{}
			require.NoError(t, json.Unmarshal(body, &env))
			assert.Equal(t, "ERR_AUTH_LOGIN_FAILED", env.Error.Code,
				"%s: wire code must be uniform", tc.name)
			assert.Equal(t, "invalid credentials", env.Error.Message,
				"%s: wire message must be uniform", tc.name)

			normalized[tc.name] = body
		})
	}

	// Three-way envelope equality (modulo request_id). If any case starts
	// emitting an extra field or a different value (even one the field-level
	// checks above wouldn't notice), this comparison fails.
	require.Len(t, normalized, len(cases),
		"all three sub-tests must record a normalized body")
	first := normalized[cases[0].name]
	for _, tc := range cases[1:] {
		assert.Equal(t, string(first), string(normalized[tc.name]),
			"401 envelope for %s must byte-equal %s (modulo request_id)", tc.name, cases[0].name)
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
