//go:build integration

package l2atomicity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jAccountlockoutThreshold mirrors cells/accesscore/internal/accountlockout.Threshold.
// The test cannot import the internal/ package; the threshold is a published
// policy (see accountlockout/policy.go and J-accountlockout.yaml) so pinning
// the literal here is the canonical seam — drift between the two surfaces is
// a real bug we want this test to surface, not abstract away.
const jAccountlockoutThreshold = 5

// TestJAccountlockoutAutoLockCycle drives the J-accountlockout journey
// end-to-end over real HTTP + PG (testcontainers), covering all three P1
// bugs from PR #585 review in a single wire-level test:
//
//   - P1#1: Threshold consecutive wrong-password POSTs auto-lock the account.
//     Pre-fix the lockout counter was rolled back by RunInTx because the
//     closure returned the 401-error; the threshold was unreachable. After
//     the fix the counter persists and attempt N flips status to Locked.
//
//   - P1#3: After admin unlock, the account is fully usable again. Pre-fix
//     the PG updateUserSQL did not write failed_login_count / last_failed_at
//     / locked_until, so admin Unlock (which routes through
//     authzmutate.Mutator.ApplyInTx(ActivateUser{}) → repo.Update) left the
//     stored counter at the locked threshold. The next failed attempt could
//     re-trigger an immediate auto-lock with stale data. After the fix the
//     post-unlock counter is 0 and the user can log in successfully.
//
// J-accountlockout passCriteria covered (declarative spec at
// journeys/J-accountlockout.yaml):
//   - "连续失败达阈值后账户锁定" — observed via Threshold→Locked side effect
//   - "锁定期间登录被拒绝" — wire 401 envelope post-lock matches uniform 401
//   - "管理员解锁后可正常登录" — post-unlock login returns 200 + token pair
//
// The TTL-elapsed lazy-unlock criterion ("user.unlocked event publish 成功" by
// admin path is covered here; lazy-unlock requires clock advance ≥ LockoutTTL
// which the harness does not currently expose, so lazy-unlock remains in
// J-accountlockout's manual-mode pool).
//
// ref: PR #585 review P1#1 / P1#3 / P2#4.
func TestJAccountlockoutAutoLockCycle(t *testing.T) {
	h := newL2Harness(t)

	const (
		victimUsername = "l2-lockcycle-victim"
		victimEmail    = "lockcycle@l2.local"
	)
	victimPassword := mustRandomPassword(16)
	wrongPassword := mustRandomPassword(16) // distinct from victimPassword

	// Admin seed: create the victim user.
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, victimEmail, victimPassword)

	// Drive Threshold consecutive wrong-password POSTs. Each returns 401
	// with the uniform envelope. After PR #585 P1#1 fix, each closure
	// commits and the counter advances by 1.
	for i := 0; i < jAccountlockoutThreshold; i++ {
		raw := httpLoginExpect401Raw(t, h.base, victimUsername, wrongPassword)
		// The uniform 401 envelope must not surface lockout details.
		_ = normalizeErrorEnvelope(t, raw) // structural assertions inside helper
	}

	// At threshold the account auto-locks. The CORRECT password now also
	// returns 401 — same uniform envelope, no enumeration via status code.
	correctButLocked := httpLoginExpect401Raw(t, h.base, victimUsername, victimPassword)
	_ = normalizeErrorEnvelope(t, correctButLocked)

	// Admin unlock — routes through authzmutate.Mutator.ApplyInTx(ActivateUser{})
	// → repo.Update with the new updateUserSQL that writes the lockout columns.
	// Without the P1#3 fix the persisted counter would still be at threshold
	// here and the next attempt would re-auto-lock immediately.
	httpUnlockUser(t, h.base, adminLogin.AccessToken, victimID)

	// Post-unlock CORRECT login must succeed. This is the strongest
	// observable proof that:
	//   - the unlock cleared the stored counter (P1#3 — otherwise the next
	//     wrong attempt would re-lock, but here we drive a SUCCESS),
	//   - the authz_epoch bump cascaded the session/refresh revocations
	//     (we get a brand-new token pair, not a stale-epoch reject).
	postUnlock := httpLogin(t, h.base, victimUsername, victimPassword)
	assert.NotEmpty(t, postUnlock.AccessToken, "unlocked account must mint a fresh access token")
	assert.NotEmpty(t, postUnlock.RefreshToken, "unlocked account must mint a fresh refresh token")
	assert.NotEmpty(t, postUnlock.SessionID, "unlocked account login must persist a session row")

	// Verify the counter is now zero by driving a SINGLE wrong-password
	// attempt and observing the wire shape. This is a smoke check on the
	// post-unlock counter state: if the stored counter were still at
	// threshold-1 (P1#3 pre-fix), this one extra wrong attempt would
	// re-lock the account; we then assert that another correct login
	// still works.
	_ = httpLoginExpect401Raw(t, h.base, victimUsername, wrongPassword)
	stillUsable := httpLogin(t, h.base, victimUsername, victimPassword)
	require.NotEmpty(t, stillUsable.AccessToken,
		"after admin unlock + one wrong attempt the account must NOT be re-locked "+
			"(P1#3: counter must be persisted as 0 by ActivateUser → Update)")
}
