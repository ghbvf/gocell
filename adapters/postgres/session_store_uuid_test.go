package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// TestPGSessionStore_NonUUIDSubjectRejected verifies that PGSessionStore.Create
// returns ErrValidationFailed when SubjectID is not a valid UUID string. This
// test exercises the validation path that fires before any DB call, so no
// testcontainer or real connection is required — a zero-value *pgxpool.Pool is
// sufficient for construction.
//
// The PG sessions table stores subject_id as a UUID FK to users.id; passing an
// opaque non-UUID string would cause a PG wire-level error that is harder to
// distinguish from transient infra faults. The early UUID validation converts
// the error to a deterministic ErrValidationFailed at the store layer.
func TestPGSessionStore_NonUUIDSubjectRejected(t *testing.T) {
	t.Parallel()

	fakePool := new(pgxpool.Pool) // non-nil, no real PG connection needed
	txm := &TxManager{}
	fc := clockmock.New(storetest.EpochAnchor())
	proto := storetest.NewTestProtocol(t)

	store, err := NewSessionStore(fakePool, txm, proto, fc)
	require.NoError(t, err)

	sess := &session.Session{
		ID:        "sess-test-id",
		SubjectID: "not-a-uuid",
		CreatedAt: storetest.EpochAnchor(),
		ExpiresAt: storetest.EpochAnchor().Add(60 * 60 * 1e9), // 1h
	}
	createErr := store.Create(context.Background(), sess)
	require.Error(t, createErr, "non-UUID SubjectID must be rejected")

	var coded *errcode.Error
	require.True(t, errors.As(createErr, &coded), "error must be *errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}
