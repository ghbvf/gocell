//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// ---------------------------------------------------------------------------
// Factory helpers
// ---------------------------------------------------------------------------

// subjectNamespace is the UUID namespace used to generate deterministic UUIDs
// from storetest subjectID strings (e.g. "subject-A" → stable UUID). This
// avoids storing TEXT subject IDs in the PG sessions.subject_id UUID column
// while keeping the integration test fixtures deterministic.
var subjectNamespace = uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

// subjectUUID maps a storetest string subject ID to a deterministic UUID.
func subjectUUID(subjectID string) uuid.UUID {
	return uuid.NewSHA1(subjectNamespace, []byte(subjectID))
}

// upsertUser inserts a user row for the given subjectID string if it does not
// already exist. The UUID is derived deterministically from subjectID so every
// Create call for the same storetest subject resolves to the same row.
//
// This is a test-only concern: the production sessions table uses a real UUID FK
// into users, but storetest drives TEXT subject IDs. We bridge the gap here
// without touching production code.
func upsertUser(t testing.TB, pool *Pool, subjectID string) uuid.UUID {
	t.Helper()
	id := subjectUUID(subjectID)
	_, err := pool.DB().Exec(context.Background(), `
INSERT INTO users (id, username, email, password_hash, status, creation_source, authz_epoch, created_at, updated_at)
VALUES ($1, $2, $3, 'x', 'active', 'identity', 0, NOW(), NOW())
ON CONFLICT (id) DO NOTHING`,
		id.String(),
		"user-"+subjectID,
		subjectID+"@test.example",
	)
	require.NoError(t, err, "upsertUser: insert user row for %s", subjectID)
	return id
}

// pgSessionStoreWrapper wraps PGSessionStore and translates TEXT subjectIDs
// from storetest fixtures to real UUIDs on write paths. The sessions table
// stores subject_id as UUID FK into users; storetest uses string identifiers
// like "subject-A". The wrapper upserts user rows on first Create and maps
// IDs transparently.
//
// This translation is not a contract violation: PGSessionStore enforces
// UUID-format SubjectID because the sessions.subject_id column is a UUID FK to
// users.id. The storetest conformance suite uses opaque strings; the wrapper
// bridges the gap by mapping opaque IDs to deterministic UUIDs at the
// integration-test layer. Mem store accepts any non-empty string and does not
// need this translation. The production path always supplies real UUID subject
// IDs (user.ID is always a UUID).
type pgSessionStoreWrapper struct {
	inner *PGSessionStore
	pool  *Pool
	t     testing.TB
}

func (w *pgSessionStoreWrapper) Create(ctx context.Context, s *session.Session) error {
	if s == nil {
		return w.inner.Create(ctx, s)
	}
	// Ensure the referenced user row exists.
	uid := upsertUser(w.t, w.pool, s.SubjectID)
	// Build a copy of the session with the UUID as SubjectID so the FK is valid.
	copy := *s
	copy.SubjectID = uid.String()
	return w.inner.Create(ctx, &copy)
}

func (w *pgSessionStoreWrapper) Get(ctx context.Context, id string) (*session.Session, error) {
	got, err := w.inner.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// Map UUID subject_id back to the storetest string ID so suite assertions
	// against Session.SubjectID match the original fixture value.
	got.SubjectID = w.resolveSubjectName(got.SubjectID)
	return got, nil
}

func (w *pgSessionStoreWrapper) Revoke(ctx context.Context, id string) error {
	return w.inner.Revoke(ctx, id)
}

func (w *pgSessionStoreWrapper) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	if subjectID == "" {
		// Let the inner store return ErrValidationFailed.
		return w.inner.RevokeForSubject(ctx, subjectID, event)
	}
	uid := subjectUUID(subjectID)
	return w.inner.RevokeForSubject(ctx, uid.String(), event)
}

// resolveSubjectName is the inverse of upsertUser: given a UUID string it
// returns the canonical storetest name. We maintain the reverse map by
// consulting the known storetest subjects (subject-A, subject-B, bench-subject).
// Unknown UUIDs are returned as-is; this is safe because the suite only
// asserts on subjects it controls.
func (w *pgSessionStoreWrapper) resolveSubjectName(uuidStr string) string {
	known := []string{"subject-A", "subject-B", "bench-subject"}
	for _, name := range known {
		if subjectUUID(name).String() == uuidStr {
			return name
		}
	}
	return uuidStr
}

// resetSessionsTable truncates sessions (and users) between subtests so each
// factory call gets a clean slate. RESTART IDENTITY resets sequences.
func resetSessionsTable(t *testing.T, pool *Pool) {
	t.Helper()
	_, err := pool.DB().Exec(context.Background(),
		"TRUNCATE sessions, users RESTART IDENTITY CASCADE")
	require.NoError(t, err, "resetSessionsTable: truncate failed")
}

// pgFactory is the storetest.Factory for the PG session store.
//
// Each call returns a fresh PGSessionStore (via wrapper) wired with a
// FakeClock anchored at storetest.EpochAnchor(). The cleanup func truncates
// the sessions + users tables so subsequent factory calls start clean.
func pgFactory(t *testing.T) (session.Store, *clockmock.FakeClock, func()) {
	t.Helper()

	pool, teardown := setupPostgres(t)
	t.Cleanup(teardown)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(pool)
	proto := storetest.NewTestProtocol(t)
	store, err := NewSessionStore(pool.DB(), txm, proto, fc)
	require.NoError(t, err)

	wrapper := &pgSessionStoreWrapper{inner: store, pool: pool, t: t}

	cleanup := func() {
		resetSessionsTable(t, pool)
	}
	return wrapper, fc, cleanup
}

// ---------------------------------------------------------------------------
// Contract suite
// ---------------------------------------------------------------------------

// TestPGSessionStore_ContractSuite runs the shared storetest contract suite
// against the PG session store backend (S3+S5). Every subtest exercises the
// Store contract defined in runtime/auth/session/store.go against a real
// PostgreSQL instance via testcontainers.
func TestPGSessionStore_ContractSuite(t *testing.T) {
	storetest.Run(t, pgFactory, storetest.NewTestProtocol(t))
}

// ---------------------------------------------------------------------------
// Constructor fail-fast cases
// ---------------------------------------------------------------------------

// TestNewSessionStore_NilPool_Rejected verifies that NewSessionStore
// fail-fasts on a nil *pgxpool.Pool. NewSessionStore checks pool == nil before
// dereferencing it, so no container is needed here.
func TestNewSessionStore_NilPool_Rejected(t *testing.T) {
	t.Parallel()
	proto := storetest.NewTestProtocol(t)
	fc := clockmock.New(storetest.EpochAnchor())
	// Use a non-nil TxManager backed by a nil pgxpool.Pool interior — the
	// nil-pool guard in NewSessionStore fires before the TxManager is used.
	txm := &TxManager{}

	store, err := NewSessionStore(nil, txm, proto, fc)
	require.Error(t, err)
	assert.Nil(t, store)
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}

// TestNewSessionStore_NilTxRunner_Rejected verifies that NewSessionStore
// fail-fasts on a typed-nil TxRunner (validation.IsNilInterface path).
// A zeroed pgxpool.Pool is used for the pool argument so the nil-pool guard
// passes and the typed-nil txRunner guard is reached.
func TestNewSessionStore_NilTxRunner_Rejected(t *testing.T) {
	t.Parallel()
	proto := storetest.NewTestProtocol(t)
	fc := clockmock.New(storetest.EpochAnchor())

	fakePool := new(pgxpool.Pool) // non-nil pointer; no actual PG connection needed
	var txr *TxManager            // typed nil — IsNilInterface should catch this
	store, err := NewSessionStore(fakePool, txr, proto, fc)
	require.Error(t, err)
	assert.Nil(t, store)
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}

// TestNewSessionStore_NilProtocol_Rejected verifies that NewSessionStore
// fail-fasts on a nil *session.Protocol.
func TestNewSessionStore_NilProtocol_Rejected(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(storetest.EpochAnchor())

	fakePool := new(pgxpool.Pool)
	txm := &TxManager{}
	store, err := NewSessionStore(fakePool, txm, nil, fc)
	require.Error(t, err)
	assert.Nil(t, store)
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}

// TestNewSessionStore_NilClock_Rejected verifies that NewSessionStore
// fail-fasts on a typed-nil clock (validation.IsNilInterface path).
func TestNewSessionStore_NilClock_Rejected(t *testing.T) {
	t.Parallel()
	proto := storetest.NewTestProtocol(t)

	fakePool := new(pgxpool.Pool)
	txm := &TxManager{}
	store, err := NewSessionStore(fakePool, txm, proto, nil)
	require.Error(t, err)
	assert.Nil(t, store)
	var coded *errcode.Error
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}
