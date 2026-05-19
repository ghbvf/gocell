// Package accountlockout is the typed mediator that drives auto-lockout
// decisions for sessionlogin.
//
// # Funnel role
//
// sessionlogin is forbidden to import authzmutate / credentialinvalidate
// directly (SESSIONLOGIN-LOCKOUT-VIA-ACCOUNTLOCKOUT-01 depguard upstream
// Hard). All sessionlogin paths that need to record a login failure / success
// / lazy-unlock go through this package, which:
//
//   - Applies the failure-window policy (Threshold + StaleWindow + LockoutTTL).
//   - Persists the auto-lockout counter via UserRepository.UpdateLockoutFields.
//   - Routes lock/unlock transitions through authzmutate.Mutator.ApplyInTx
//     (LockUser → epoch bump + session/refresh revoke; ActivateUser →
//     additive status flip).
//   - Emits event.user.locked.v1 (ActorID="system") on auto-lock and
//     event.user.unlocked.v1 (ActorID="system") on lazy-unlock — both via
//     publishLocked / publishUnlocked, both wired through the same outbox
//     emitter, both observable through `auth_account_lockout_total{reason}`
//     counter + slog. ActorID="system" lets consumers (auditcore /
//     downstream projectors) distinguish framework-driven mutations from
//     admin-initiated lock/unlock without breaking wire-shape symmetry.
//
// # Policy constants (hardcoded, no Policy struct — YAGNI per plan §Context)
//
// Per-tenant configurability is registered as backlog
// ACCOUNT-LOCKOUT-POLICY-CONFIGURABLE-01 (P3, triggered when a real per-tenant
// requirement appears).
package accountlockout

import "time"

// Threshold is the number of consecutive failed logins (within StaleWindow)
// that trigger an auto-lock.
//
// Keycloak `failureFactor` default = 30; ASP.NET Identity
// `MaxFailedAccessAttempts` default = 5. GoCell picks 5 to match ASP.NET
// (stricter, biases toward fail-closed; consistent with the project's
// fail-closed principle).
const Threshold = 5

// StaleWindow is the idle gap that resets the failure counter.
//
// If LastFailedAt is older than StaleWindow when a new failure arrives, the
// counter is reset to 1 (treated as a fresh attempt sequence) — equivalent to
// Keycloak's `maxDeltaTimeSeconds` failure-reset-on-inactivity, with a tighter
// default to keep the policy responsive.
const StaleWindow = 15 * time.Minute

// LockoutTTL is the lazy-unlock TTL applied on auto-lock.
//
// When the user's `locked_until` has elapsed, sessionlogin transparently
// unlocks (status=Active + counter=0 + locked_until=NULL) inside the login
// tx and continues bcrypt verification. Aligns with Keycloak
// `maxFailureWaitSeconds` cap (15 min) and is identical to StaleWindow to
// minimize const surface.
const LockoutTTL = 15 * time.Minute

// SystemActorID is the ActorID stamped on auto-emitted event.user.locked.v1
// when the lock is triggered by accountlockout (not by an admin). The wire
// payload uses this string so consumers (e.g., auditcore) can distinguish
// automatic locks from admin-initiated locks without a schema change.
const SystemActorID = "system"
