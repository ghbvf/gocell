// Package storetest provides a reusable Protocol-driven contract test suite for
// session.Store implementations. Each backend (mem, postgres) supplies a
// Factory and runs Run with the same Protocol; the suite derives test cases
// from Protocol.RevokeOn() and Protocol.Fingerprint() so every backend is
// proved to honor the same protocol decisions (ADR-Session §4.3).
//
// Helpers (NewTestProtocol / NewSessionFixture) are exported so future PG
// store integration tests in S3+S5 reuse the same fixture surface; the path
// runtime/auth/session/storetest/ is in the SESSION-PROTOCOL-COMPOSITION-ROOT-01
// archtest allowlist so calls to session.NewProtocol from this package are
// permitted.
//
// All test backends share NewTestProtocol so they prove parity on the same
// protocol decisions; backends differ only in their Factory implementation.
package storetest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Factory constructs a fresh Store with a deterministic clock. Backends with
// per-test setup (e.g. PG schema reset) do it inside Factory; cleanup is the
// returned func and must be safe to call exactly once.
//
// The fakeClock return type is the concrete *clockmock.FakeClock rather than
// the clock.Clock interface — suite cases call fc.Advance() and fc.Now()
// directly, methods that only the concrete type carries. PG store factories
// in S3+S5 must therefore also construct and return *clockmock.FakeClock,
// even when wiring it through the store as a clock.Clock at construction.
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

// validateFixtureInputs centralizes the argument-validation rules used by
// NewSessionFixture so the fatal vs. non-fatal split has a single source of
// truth and the error-path is unit-testable without forking testing.TB.
func validateFixtureInputs(subjectID, jti string, ttl time.Duration) error {
	if subjectID == "" {
		return errors.New("storetest: NewSessionFixture requires non-empty subjectID")
	}
	if jti == "" {
		return errors.New("storetest: NewSessionFixture requires non-empty jti")
	}
	if ttl <= 0 {
		return errors.New("storetest: NewSessionFixture requires positive ttl")
	}
	return nil
}

// NewSessionFixture constructs a Session with deterministic timestamps derived
// from now + ttl. Callers control SubjectID / JTI / epoch explicitly so cases
// can assert RevokeForSubject scoping precisely. ID is derived from JTI to
// keep cases readable (Session.ID is opaque to the protocol).
func NewSessionFixture(t *testing.T, subjectID, jti string, epoch int64, ttl time.Duration, now time.Time) *session.Session {
	t.Helper()
	if err := validateFixtureInputs(subjectID, jti, ttl); err != nil {
		t.Fatal(err)
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
	t.Run("RevokeForSubject_EmptySubject_Rejected", func(t *testing.T) {
		runRevokeForSubjectEmptySubject(t, factory)
	})
	t.Run("RevokeForSubject_UnknownEvent_Rejected", func(t *testing.T) {
		runRevokeForSubjectUnknownEvent(t, factory)
	})

	for _, event := range protocol.RevokeOn() {
		event := event
		t.Run("RevokeForSubject_"+event.String(), func(t *testing.T) {
			runRevokeForSubject(t, factory, event)
		})
	}

	if _, ok := protocol.Fingerprint().(session.FingerprintJTIRef); ok {
		t.Run("Fingerprint_JTI_NoPlaintextToken", func(t *testing.T) {
			runFingerprintJTINoPlaintext(t)
		})
	}
}

const (
	caseTTL                 = time.Hour
	caseExpiryAdvance       = 2 * caseTTL
	caseEpoch         int64 = 7
	subjectA                = "subject-A"
	subjectB                = "subject-B"
	// fatal-format strings used by ≥3 cases — go-standards.md "同义字符串 ≥3 次抽常量".
	errFmtCreate = "Create: %v"
	errFmtGet    = "Get %s: %v"
)

// runCreateGet — Create persists the record; Get returns a defensive copy
// that round-trips every field through the store.
func runCreateGet(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	fixture := NewSessionFixture(t, subjectA, "jti-create-get", caseEpoch, caseTTL, fc.Now())
	if err := store.Create(context.Background(), fixture); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}
	got, err := store.Get(context.Background(), fixture.ID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got == fixture {
		t.Error("Get must return a defensive copy, not the original pointer")
	}
	if got.JTI != fixture.JTI {
		t.Errorf("JTI: got %q, want %q", got.JTI, fixture.JTI)
	}
	if got.SubjectID != fixture.SubjectID {
		t.Errorf("SubjectID: got %q, want %q", got.SubjectID, fixture.SubjectID)
	}
	if got.AuthzEpochAtIssue != fixture.AuthzEpochAtIssue {
		t.Errorf("AuthzEpochAtIssue: got %d, want %d", got.AuthzEpochAtIssue, fixture.AuthzEpochAtIssue)
	}
	if !got.CreatedAt.Equal(fixture.CreatedAt) {
		t.Errorf("CreatedAt: got %s, want %s", got.CreatedAt, fixture.CreatedAt)
	}
	if !got.ExpiresAt.Equal(fixture.ExpiresAt) {
		t.Errorf("ExpiresAt: got %s, want %s", got.ExpiresAt, fixture.ExpiresAt)
	}
	if got.RevokedAt != nil {
		t.Errorf("RevokedAt: got %v, want nil", got.RevokedAt)
	}
}

// runGetNotFound — missing IDs return ErrSessionNotFound (errcode.Code).
func runGetNotFound(t *testing.T, factory Factory) {
	store, _, cleanup := factory(t)
	defer cleanup()

	got, err := store.Get(context.Background(), "missing-id")
	if got != nil {
		t.Errorf("expected nil session on miss, got %+v", got)
	}
	assertErrCode(t, err, errcode.ErrSessionNotFound)
}

// runCreateDuplicateID — duplicate Session.ID is rejected with
// ErrSessionConflict (防枚举 / KindConflict envelope).
func runCreateDuplicateID(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	fixture := NewSessionFixture(t, subjectA, "jti-dup", caseEpoch, caseTTL, fc.Now())
	if err := store.Create(context.Background(), fixture); err != nil {
		t.Fatalf("first Create unexpected error: %v", err)
	}
	err := store.Create(context.Background(), fixture)
	assertErrCode(t, err, errcode.ErrSessionConflict)
}

// runRevokeDirect — Revoke flips RevokedAt to clock.Now(); subsequent Get
// returns the same session with RevokedAt set.
func runRevokeDirect(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	fixture := NewSessionFixture(t, subjectA, "jti-revoke", caseEpoch, caseTTL, fc.Now())
	if err := store.Create(context.Background(), fixture); err != nil {
		t.Fatalf(errFmtCreate, err)
	}
	revokeAt := fc.Now()
	if err := store.Revoke(context.Background(), fixture.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, err := store.Get(context.Background(), fixture.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RevokedAt == nil {
		t.Fatal("RevokedAt: expected non-nil after Revoke")
	}
	if !got.RevokedAt.Equal(revokeAt) {
		t.Errorf("RevokedAt: got %s, want %s", got.RevokedAt, revokeAt)
	}
}

// runRevokeIdempotent — second Revoke on the same ID is a no-op (RevokedAt
// timestamp must not be re-stamped; append-only revoke semantics).
func runRevokeIdempotent(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	fixture := NewSessionFixture(t, subjectA, "jti-idem", caseEpoch, caseTTL, fc.Now())
	if err := store.Create(context.Background(), fixture); err != nil {
		t.Fatalf(errFmtCreate, err)
	}
	firstRevokeAt := fc.Now()
	if err := store.Revoke(context.Background(), fixture.ID); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}

	fc.Advance(testtime.D5min)

	if err := store.Revoke(context.Background(), fixture.ID); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	got, err := store.Get(context.Background(), fixture.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RevokedAt == nil {
		t.Fatal("RevokedAt: expected non-nil after Revoke")
	}
	if !got.RevokedAt.Equal(firstRevokeAt) {
		t.Errorf("RevokedAt re-stamped on second Revoke: got %s, want %s (append-only)", got.RevokedAt, firstRevokeAt)
	}
}

// runRevokeNotFoundNoop — Revoke of a missing ID returns nil (防枚举 — caller
// cannot distinguish "session existed, now revoked" from "never existed").
func runRevokeNotFoundNoop(t *testing.T, factory Factory) {
	store, _, cleanup := factory(t)
	defer cleanup()

	if err := store.Revoke(context.Background(), "ghost-id"); err != nil {
		t.Errorf("Revoke on missing ID must be no-op nil, got %v", err)
	}
}

// runExpiredStillReturned — expired sessions are still returned by Get; the
// store does not garbage-collect or hide them. Caller decides via
// Session.ExpiresAt comparison.
func runExpiredStillReturned(t *testing.T, factory Factory) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	fixture := NewSessionFixture(t, subjectA, "jti-exp", caseEpoch, caseTTL, fc.Now())
	if err := store.Create(context.Background(), fixture); err != nil {
		t.Fatalf(errFmtCreate, err)
	}
	fc.Advance(caseExpiryAdvance) // past expiry

	got, err := store.Get(context.Background(), fixture.ID)
	if err != nil {
		t.Fatalf("Get on expired must still succeed, got %v", err)
	}
	if !fc.Now().After(got.ExpiresAt) {
		t.Error("clock did not advance past ExpiresAt; test setup wrong")
	}
	if got.RevokedAt != nil {
		t.Error("expiry must not auto-revoke; RevokedAt should remain nil")
	}
}

// runRevokeForSubjectEmptySubject — Store contract: empty subjectID is
// rejected with ErrValidationFailed (Store interface godoc). PG store
// (S3+S5) must conform.
func runRevokeForSubjectEmptySubject(t *testing.T, factory Factory) {
	store, _, cleanup := factory(t)
	defer cleanup()

	err := store.RevokeForSubject(context.Background(), "", session.CredentialEventPasswordReset)
	assertErrCode(t, err, errcode.ErrValidationFailed)
}

// runRevokeForSubjectUnknownEvent — Store contract: a CredentialEvent value
// outside the declared enum is rejected with ErrValidationFailed even when
// the Protocol-level WithRevokeOnAll covers the canonical 4 (defense in
// depth — Store callers may obtain CredentialEvent from external sources).
func runRevokeForSubjectUnknownEvent(t *testing.T, factory Factory) {
	store, _, cleanup := factory(t)
	defer cleanup()

	err := store.RevokeForSubject(context.Background(), subjectA, session.CredentialEvent(99))
	assertErrCode(t, err, errcode.ErrValidationFailed)
}

// revokeForSubjectFixtures bundles the four session fixtures used by the
// per-event RevokeForSubject case so they can be threaded through helpers
// without exploding the parent function's signature.
type revokeForSubjectFixtures struct {
	a1, a2, aRevoked, b *session.Session
}

// seedRevokeForSubjectFixtures creates the case fixtures, persists them, and
// performs the pre-revoke on aRevoked so caller can assert that
// RevokeForSubject does not re-stamp its RevokedAt timestamp.
func seedRevokeForSubjectFixtures(
	t *testing.T,
	store session.Store,
	fc *clockmock.FakeClock,
	event session.CredentialEvent,
) (revokeForSubjectFixtures, time.Time) {
	t.Helper()
	ctx := context.Background()
	fix := revokeForSubjectFixtures{
		a1:       NewSessionFixture(t, subjectA, "jti-a1-"+event.String(), caseEpoch, caseTTL, fc.Now()),
		a2:       NewSessionFixture(t, subjectA, "jti-a2-"+event.String(), caseEpoch, caseTTL, fc.Now()),
		aRevoked: NewSessionFixture(t, subjectA, "jti-aRevoked-"+event.String(), caseEpoch, caseTTL, fc.Now()),
		b:        NewSessionFixture(t, subjectB, "jti-b-"+event.String(), caseEpoch, caseTTL, fc.Now()),
	}
	for _, s := range []*session.Session{fix.a1, fix.a2, fix.aRevoked, fix.b} {
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create %s: %v", s.ID, err)
		}
	}
	preRevokeAt := fc.Now()
	if err := store.Revoke(ctx, fix.aRevoked.ID); err != nil {
		t.Fatalf("pre-Revoke: %v", err)
	}
	return fix, preRevokeAt
}

// assertActiveSessionsRevoked asserts every supplied session is now revoked
// at exactly want; non-revoked or off-timestamp sessions are reported per ID.
func assertActiveSessionsRevoked(t *testing.T, store session.Store, want time.Time, fixtures ...*session.Session) {
	t.Helper()
	ctx := context.Background()
	for _, fix := range fixtures {
		got, err := store.Get(ctx, fix.ID)
		if err != nil {
			t.Fatalf(errFmtGet, fix.ID, err)
		}
		if got.RevokedAt == nil {
			t.Errorf("subjectA session %s: expected RevokedAt non-nil after RevokeForSubject", fix.ID)
			continue
		}
		if !got.RevokedAt.Equal(want) {
			t.Errorf("subjectA session %s: RevokedAt = %s, want %s", fix.ID, got.RevokedAt, want)
		}
	}
}

// assertSessionRevokedAtUnchanged asserts the session keyed by id has
// RevokedAt == want (defensive — pre-revoked rows must not be re-stamped).
func assertSessionRevokedAtUnchanged(t *testing.T, store session.Store, id string, want time.Time) {
	t.Helper()
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf(errFmtGet, id, err)
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(want) {
		t.Errorf("pre-revoked session %s: RevokedAt re-stamped to %v, want preserved at %s", id, got.RevokedAt, want)
	}
}

// assertSessionUnrevoked asserts the session keyed by id has nil RevokedAt
// (subject-scope isolation — RevokeForSubject(subjectA) must leave subjectB's
// session untouched).
func assertSessionUnrevoked(t *testing.T, store session.Store, id string) {
	t.Helper()
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf(errFmtGet, id, err)
	}
	if got.RevokedAt != nil {
		t.Errorf("session %s must remain active after RevokeForSubject(other-subject), got RevokedAt %v", id, got.RevokedAt)
	}
}

// runRevokeForSubject — revokes every active session for subjectA on the
// given event; subjectB's session must remain untouched. Already-revoked
// sessions for subjectA must keep their original RevokedAt timestamp.
func runRevokeForSubject(t *testing.T, factory Factory, event session.CredentialEvent) {
	store, fc, cleanup := factory(t)
	defer cleanup()

	fix, preRevokeAt := seedRevokeForSubjectFixtures(t, store, fc, event)

	fc.Advance(time.Minute) // ensure subsequent revoke would have a distinct timestamp
	revokeAt := fc.Now()

	if err := store.RevokeForSubject(context.Background(), subjectA, event); err != nil {
		t.Fatalf("RevokeForSubject(%s, %s): %v", subjectA, event, err)
	}

	assertActiveSessionsRevoked(t, store, revokeAt, fix.a1, fix.a2)
	assertSessionRevokedAtUnchanged(t, store, fix.aRevoked.ID, preRevokeAt)
	assertSessionUnrevoked(t, store, fix.b.ID)
}

// runFingerprintJTINoPlaintext is Protocol-Fingerprint-driven: under
// FingerprintJTIRef the Session struct must NOT carry any plaintext-token
// shaped field. The check is a reflective field-name audit so backends
// adding their own Session decorations cannot regress D1.
//
// Match semantics: strings.EqualFold case-insensitive whole-name match
// against the forbidden list. We do NOT match on substring (e.g. a future
// "TokenID" field naming an opaque server-side identifier would not
// trip this guard) — D1 cares about token-shaped *plaintext storage*, not
// about any field whose name happens to contain "token". If a future field
// must collide with a forbidden name, it is the field author's burden to
// rename or to extend the forbidden list intentionally with a comment.
func runFingerprintJTINoPlaintext(t *testing.T) {
	for _, problem := range auditFingerprintJTIShape(reflect.TypeOf(session.Session{})) {
		t.Error(problem)
	}
}

// fingerprintJTIForbidden lists field names whose presence on the Session
// struct would constitute plaintext-token storage under FingerprintJTIRef
// (ADR-Session D1).
var fingerprintJTIForbidden = []string{"AccessToken", "Token", "Plaintext", "Secret", "Password"}

// auditFingerprintJTIShape returns a list of human-readable problems for
// Session-shaped types under the FingerprintJTIRef mode. Splitting the audit
// from the t.Error reporting lets internal tests exercise both legal and
// illegal struct shapes without forking testing.TB.
func auditFingerprintJTIShape(st reflect.Type) []string {
	var problems []string
	for i := 0; i < st.NumField(); i++ {
		name := st.Field(i).Name
		for _, bad := range fingerprintJTIForbidden {
			if strings.EqualFold(name, bad) {
				problems = append(problems, "Session."+name+" violates FingerprintJTIRef D1 (no plaintext token shape)")
			}
		}
	}
	jtiField, ok := st.FieldByName("JTI")
	switch {
	case !ok:
		problems = append(problems, "Session.JTI field missing under FingerprintJTIRef")
	case jtiField.Type.Kind() != reflect.String:
		problems = append(problems, "Session.JTI must be string, got "+jtiField.Type.Kind().String())
	}
	return problems
}

// assertErrCode asserts err wraps an *errcode.Error with the given Code.
// Centralized here so storetest cases stay focused on protocol semantics.
func assertErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error with code %s, got %T: %v", want, err, err)
	}
	if coded.Code != want {
		t.Errorf("errcode mismatch: got %s, want %s (msg=%q)", coded.Code, want, coded.Message)
	}
}
