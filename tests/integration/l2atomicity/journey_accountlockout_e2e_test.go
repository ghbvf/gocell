//go:build integration

package l2atomicity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// jAccountlockoutThreshold mirrors corecells/accesscore/internal/accountlockout.Threshold.
// The test cannot import the internal/ package, so we pin the literal here.
// This is a known soft seam — drift between the two surfaces is NOT detected
// at build/CI time. Do not silently change one surface without the other.
const eventUserLockedV1 = "event.user.locked.v1"

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
// journeys/J-accountlockout.yaml). Each criterion has an explicit observable
// in this test:
//
//   - "连续失败达阈值后账户锁定" — Threshold-th wrong-password attempt is the
//     last 401 from the loop; the next CORRECT-password attempt also gets 401,
//     proving the account is now locked (no other explanation for the same
//     password switching from accepted to rejected).
//   - "user.locked 事件发布成功" — the auditcore consumer subscribes to
//     event.user.locked.v1; we assert at least one audit-ledger entry for
//     that event type appears within a short Eventually window after the
//     threshold attempt (proves the outbox relay + auditcore handler
//     actually saw the event, not just that publishLocked was called).
//   - "锁定期间登录被拒绝" — the locked-account 401 envelope is byte-equal
//     (modulo request_id) to a wrong-password 401 — anti-enumeration check
//     local to the auto-lock path, complementing TestL2_LoginUniform401's
//     coverage of the admin-lock path.
//   - "管理员解锁后可正常登录" — post-unlock CORRECT-password login returns
//     a fresh access/refresh/session triple; one wrong attempt after that
//     does not re-lock (the counter was actually persisted as 0 by
//     ActivateUser → Update, not stuck at threshold-1).
//
// The TTL-elapsed lazy-unlock criterion (`user.unlocked` event publish on
// TTL expiry, not on admin unlock) remains in J-accountlockout's manual-mode
// pool because LockoutTTL=15min and the l2 harness uses clock.Real(). Do not
// silently revert this to mode: auto without a clock-advance seam.
//
// ref: PR #585 review P1#1 / P1#3 / P2#4 / P2#12 / P2-F1.
func TestJAccountlockoutAutoLockCycle(t *testing.T) {
	h := newL2Harness(t)
	ctx := context.Background()

	const (
		victimUsername = "l2-lockcycle-victim"
		victimEmail    = "lockcycle@l2.local"
	)
	victimPassword := mustRandomPassword(16)
	wrongPassword := mustRandomPassword(16) // distinct from victimPassword

	// Admin seed: create the victim user.
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, victimEmail, victimPassword)

	// Baseline audit count for the locked-event topic — auditcore may have
	// other event-bus chatter; we only care about the increment caused by
	// THIS test's auto-lock.
	lockedAuditBaseline := countAuditEntries(t, ctx, h, eventUserLockedV1)

	// Drive Threshold consecutive wrong-password POSTs. Each returns 401
	// with the uniform envelope. After PR #585 P1#1 fix, each closure
	// commits and the counter advances by 1.
	var lastWrongPasswordNormalized []byte
	for i := 0; i < jAccountlockoutThreshold; i++ {
		raw := httpLoginExpect401Raw(t, h.base, victimUsername, wrongPassword)
		lastWrongPasswordNormalized = normalizeErrorEnvelope(t, raw)
	}

	// At threshold the account auto-locks. The CORRECT password now also
	// returns 401 — same uniform envelope, no enumeration via status code.
	correctButLockedRaw := httpLoginExpect401Raw(t, h.base, victimUsername, victimPassword)
	correctButLockedNormalized := normalizeErrorEnvelope(t, correctButLockedRaw)

	// PR #585 review F12: assert the auto-locked 401 envelope is byte-equal
	// (modulo request_id) to a wrong-password 401 from the same test. This
	// is auto-lock-path-specific anti-enumeration: TestL2_LoginUniform401
	// covers admin-lock; here we lock the bytes for the auto-lock path.
	assert.Equal(t, string(lastWrongPasswordNormalized), string(correctButLockedNormalized),
		"auto-locked 401 envelope must byte-equal wrong-password 401 envelope (anti-enumeration)")

	// PR #585 review F1: assert event.user.locked.v1 actually reached the
	// outbox + relay + auditcore consumer. Without this Eventually the
	// "user.locked 事件发布成功" criterion would only be inferred from the
	// status-flip side effect — a noop emitter would silently satisfy the
	// rest of the test.
	testwait.External(t, "l2-user-locked-event-audited", func() bool {
		return countAuditEntries(t, ctx, h, eventUserLockedV1) > lockedAuditBaseline
	}, testtime.EventuallyLong, testtime.D100ms,
		"event.user.locked.v1 must be published to outbox and consumed by auditcore on auto-lock")

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
