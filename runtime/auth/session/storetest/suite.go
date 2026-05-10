// Package storetest provides a reusable Protocol-driven contract test suite for
// session.Store implementations. Each backend (mem, postgres) supplies a
// Factory and runs Run with the same Protocol; the suite derives test cases
// from Protocol.RevokeOn() and Protocol.Fingerprint() so every backend is
// proved to honour the same protocol decisions (ADR-Session §4.3).
//
// Helpers (NewTestProtocol / NewSessionFixture) are exported so future PG
// store integration tests in S3+S5 reuse the same fixture surface; the path
// runtime/auth/session/storetest/ is in the SESSION-PROTOCOL-COMPOSITION-ROOT-01
// archtest allowlist so calls to session.NewProtocol from this package are
// permitted.
package storetest

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Factory constructs a fresh Store with a deterministic clock. Backends with
// per-test setup (e.g. PG schema reset) do it inside Factory; cleanup is the
// returned func and must be safe to call exactly once.
type Factory func(t *testing.T) (store session.Store, fakeClock *clockmock.FakeClock, cleanup func())

// epochAnchor is the deterministic start time used by NewTestProtocol-driven
// fixtures. Anchored at 2025-01-01 UTC (round, far from epoch boundaries).
var epochAnchor = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// EpochAnchor returns the deterministic clock anchor used by storetest cases;
// backends constructing FakeClock from outside the suite (per-test setup hooks)
// should use this exact value so case timestamps line up.
func EpochAnchor() time.Time { return epochAnchor }

// NewTestProtocol constructs the canonical S2 protocol shape: jti-only
// fingerprint (D1) + AuthzEpoch ordering (D2) + all 4 CredentialEvent values
// declared (D3 fail-closed). This call routes through session.NewProtocol; the
// archtest SESSION-PROTOCOL-COMPOSITION-ROOT-01 allowlist must include
// runtime/auth/session/storetest/ for this to compile-link cleanly.
func NewTestProtocol(t *testing.T) *session.Protocol {
	t.Helper()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		t.Fatalf("storetest: NewTestProtocol failed: %v", err)
	}
	return p
}

// NewSessionFixture constructs a Session with deterministic timestamps derived
// from now + ttl. Callers control SubjectID / JTI / epoch explicitly so cases
// can assert RevokeForSubject scoping precisely. ID is derived from JTI to
// keep cases readable (Session.ID is opaque to the protocol).
func NewSessionFixture(t *testing.T, subjectID, jti string, epoch int64, ttl time.Duration, now time.Time) *session.Session {
	t.Helper()
	if subjectID == "" {
		t.Fatalf("storetest: NewSessionFixture requires non-empty subjectID")
	}
	if jti == "" {
		t.Fatalf("storetest: NewSessionFixture requires non-empty jti")
	}
	if ttl <= 0 {
		t.Fatalf("storetest: NewSessionFixture requires positive ttl, got %s", ttl)
	}
	return &session.Session{
		ID:                "sess-" + jti,
		SubjectID:         subjectID,
		JTI:               jti,
		AuthzEpochAtIssue: epoch,
		CreatedAt:         now,
		ExpiresAt:         now.Add(ttl),
	}
}

// Run executes the Protocol-driven contract suite against factory. The suite
// runs the always-on cases unconditionally and derives one case per declared
// CredentialEvent in protocol.RevokeOn(). Fingerprint shape conformance keys
// off the runtime type of protocol.Fingerprint().
func Run(t *testing.T, factory Factory, protocol *session.Protocol) {
	t.Helper()
	if factory == nil {
		t.Fatal("storetest.Run: factory must not be nil")
	}
	if protocol == nil {
		t.Fatal("storetest.Run: protocol must not be nil")
	}

	t.Run("Create_Get", func(t *testing.T) { runCreateGet(t, factory) })
	t.Run("Get_NotFound", func(t *testing.T) { runGetNotFound(t, factory) })
	t.Run("Create_DuplicateID", func(t *testing.T) { runCreateDuplicateID(t, factory) })
	t.Run("Revoke_Direct", func(t *testing.T) { runRevokeDirect(t, factory) })
	t.Run("Revoke_Idempotent", func(t *testing.T) { runRevokeIdempotent(t, factory) })
	t.Run("Revoke_NotFound_Noop", func(t *testing.T) { runRevokeNotFoundNoop(t, factory) })
	t.Run("Expired_StillReturned", func(t *testing.T) { runExpiredStillReturned(t, factory) })

	for _, event := range protocol.RevokeOn() {
		event := event
		t.Run("RevokeForSubject_"+event.String(), func(t *testing.T) {
			runRevokeForSubject(t, factory, event)
		})
	}

	switch protocol.Fingerprint().(type) {
	case session.FingerprintJTIRef:
		t.Run("Fingerprint_JTI_NoPlaintextToken", func(t *testing.T) {
			runFingerprintJTINoPlaintext(t)
		})
	}
}

// runCreateGet — Wave 1 RED stub. Wave 2 fills the body.
func runCreateGet(t *testing.T, _ Factory) { t.Skip("RED: storetest body lands in Wave 2") }

// runGetNotFound — Wave 1 RED stub.
func runGetNotFound(t *testing.T, _ Factory) { t.Skip("RED: storetest body lands in Wave 2") }

// runCreateDuplicateID — Wave 1 RED stub.
func runCreateDuplicateID(t *testing.T, _ Factory) { t.Skip("RED: storetest body lands in Wave 2") }

// runRevokeDirect — Wave 1 RED stub.
func runRevokeDirect(t *testing.T, _ Factory) { t.Skip("RED: storetest body lands in Wave 2") }

// runRevokeIdempotent — Wave 1 RED stub.
func runRevokeIdempotent(t *testing.T, _ Factory) { t.Skip("RED: storetest body lands in Wave 2") }

// runRevokeNotFoundNoop — Wave 1 RED stub.
func runRevokeNotFoundNoop(t *testing.T, _ Factory) { t.Skip("RED: storetest body lands in Wave 2") }

// runExpiredStillReturned — Wave 1 RED stub.
func runExpiredStillReturned(t *testing.T, _ Factory) { t.Skip("RED: storetest body lands in Wave 2") }

// runRevokeForSubject — Wave 1 RED stub.
func runRevokeForSubject(t *testing.T, _ Factory, _ session.CredentialEvent) {
	t.Skip("RED: storetest body lands in Wave 2")
}

// runFingerprintJTINoPlaintext — Wave 1 RED stub.
func runFingerprintJTINoPlaintext(t *testing.T) {
	t.Skip("RED: storetest body lands in Wave 2")
}
