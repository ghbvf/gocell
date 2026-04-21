package postgres

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// NewRefreshStore constructor panics
// ---------------------------------------------------------------------------

func TestNewRefreshStore_Panics(t *testing.T) {
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour}

	// dummyPool is a non-nil *pgxpool.Pool used only to pass the nil check in
	// the constructor — it is never used for actual DB calls in these tests.
	dummyPool := new(pgxpool.Pool)

	t.Run("nil_pool", func(t *testing.T) {
		assert.Panics(t, func() {
			NewRefreshStore(nil, validPolicy, validClock, nil)
		})
	})

	t.Run("nil_clock", func(t *testing.T) {
		assert.Panics(t, func() {
			NewRefreshStore(dummyPool, validPolicy, nil, nil)
		})
	})

	t.Run("zero_MaxAge", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: time.Second, MaxAge: 0}
		assert.Panics(t, func() {
			NewRefreshStore(dummyPool, p, validClock, nil)
		})
	})

	t.Run("negative_MaxAge", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: time.Second, MaxAge: -time.Hour}
		assert.Panics(t, func() {
			NewRefreshStore(dummyPool, p, validClock, nil)
		})
	})

	t.Run("negative_ReuseInterval", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: -time.Second, MaxAge: time.Hour}
		assert.Panics(t, func() {
			NewRefreshStore(dummyPool, p, validClock, nil)
		})
	})
}

func TestNewRefreshStore_NilRandReader_UsesDefault(t *testing.T) {
	dummyPool := new(pgxpool.Pool)
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour}

	// nil randReader must not panic — constructor falls back to crypto/rand.Reader.
	assert.NotPanics(t, func() {
		s := NewRefreshStore(dummyPool, validPolicy, validClock, nil)
		assert.NotNil(t, s.rand, "rand field must be non-nil after constructor")
	})
}

// ---------------------------------------------------------------------------
// generateTokenID — pure function, controllable via io.Reader
// ---------------------------------------------------------------------------

func TestGenerateTokenID_Length(t *testing.T) {
	// 32 zero bytes → base64url(32 bytes) = 43 chars (RawURLEncoding, no padding).
	src := bytes.NewReader(make([]byte, 32))
	s := &PGRefreshStore{rand: src}

	id, err := s.generateTokenID()
	require.NoError(t, err)
	assert.Len(t, id, 43, "token ID must be exactly 43 characters")
}

func TestGenerateTokenID_ReaderError(t *testing.T) {
	sentinel := errors.New("entropy source exhausted")
	s := &PGRefreshStore{rand: &errorReader{err: sentinel}}

	_, err := s.generateTokenID()
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "reader error must be wrapped and surfaced")
}

// ---------------------------------------------------------------------------
// scanTokenRow / scanFullTokenRow — RowScanner stub
// ---------------------------------------------------------------------------

func TestScanTokenRow_ErrNoRows(t *testing.T) {
	row := &stubRow{err: pgx.ErrNoRows}
	tok, err := scanTokenRow(row)
	assert.Nil(t, tok)
	assert.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestScanTokenRow_ScanError(t *testing.T) {
	sentinel := errors.New("db scan failure")
	row := &stubRow{err: sentinel}
	tok, err := scanTokenRow(row)
	assert.Nil(t, tok)
	assert.ErrorIs(t, err, sentinel)
}

func TestScanTokenRow_NilObsoleteToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := newScanRow(
		int64(1),           // id
		"tok-abc",          // token
		(*string)(nil),     // obsolete_token NULL
		"sess-1",           // session_id
		"user-1",           // subject_id
		now,                // created_at
		now,                // last_used
		now.Add(time.Hour), // expires_at
	)
	tok, err := scanTokenRow(row)
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.Equal(t, "tok-abc", tok.ID)
	assert.Empty(t, tok.ObsoleteToken, "nil obsolete_token must map to empty string")
	assert.Equal(t, "sess-1", tok.SessionID)
	assert.Equal(t, "user-1", tok.SubjectID)
}

func TestScanTokenRow_WithObsoleteToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := "old-tok"
	row := newScanRow(
		int64(2),
		"tok-def",
		&prev,
		"sess-2",
		"user-2",
		now,
		now,
		now.Add(time.Hour),
	)
	tok, err := scanTokenRow(row)
	require.NoError(t, err)
	assert.Equal(t, "old-tok", tok.ObsoleteToken)
}

func TestScanFullTokenRow_ErrNoRows(t *testing.T) {
	row := &stubRow{err: pgx.ErrNoRows}
	tok, err := scanFullTokenRow(row)
	assert.Nil(t, tok)
	assert.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestScanFullTokenRow_WithObsoleteToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := "old-tok-full"
	row := newFullScanRow(
		int64(4), "tok-jkl", &prev, "sess-4", "user-4",
		now, now, now.Add(time.Hour), nil,
	)
	tok, err := scanFullTokenRow(row)
	require.NoError(t, err)
	assert.Equal(t, "old-tok-full", tok.ObsoleteToken)
}

func TestScanFullTokenRow_WithRevokedAt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	revoked := now.Add(-time.Minute)
	row := newFullScanRow(
		int64(3),
		"tok-ghi",
		(*string)(nil),
		"sess-3",
		"user-3",
		now,
		now,
		now.Add(time.Hour),
		&revoked,
	)
	tok, err := scanFullTokenRow(row)
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.Equal(t, "tok-ghi", tok.ID)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// errorReader always returns the configured error from Read.
type errorReader struct{ err error }

func (r *errorReader) Read(_ []byte) (int, error) { return 0, r.err }

// stubRow returns a fixed error from Scan, simulating DB failure or no-rows.
type stubRow struct{ err error }

func (r *stubRow) Scan(_ ...any) error { return r.err }

// scanRow holds pre-set values and populates dest pointers in order.
// Supports: *int64, *string, **string, *time.Time, **time.Time.
type scanRow struct{ vals []any }

func (r *scanRow) Scan(dest ...any) error {
	for i, d := range dest {
		if i >= len(r.vals) {
			break
		}
		switch dp := d.(type) {
		case *int64:
			if v, ok := r.vals[i].(int64); ok {
				*dp = v
			}
		case *string:
			if v, ok := r.vals[i].(string); ok {
				*dp = v
			}
		case **string:
			if v, ok := r.vals[i].(*string); ok {
				*dp = v
			}
		case *time.Time:
			if v, ok := r.vals[i].(time.Time); ok {
				*dp = v
			}
		case **time.Time:
			if v, ok := r.vals[i].(*time.Time); ok {
				*dp = v
			}
		}
	}
	return nil
}

// newScanRow constructs a scanRow for scanTokenRow (8 columns).
func newScanRow(id int64, token string, obsolete *string, session, subject string, created, lastUsed, expires time.Time) *scanRow {
	return &scanRow{vals: []any{id, token, obsolete, session, subject, created, lastUsed, expires}}
}

// newFullScanRow constructs a scanRow for scanFullTokenRow (9 columns, includes revokedAt).
func newFullScanRow(id int64, token string, obsolete *string, session, subject string, created, lastUsed, expires time.Time, revokedAt *time.Time) *scanRow {
	return &scanRow{vals: []any{id, token, obsolete, session, subject, created, lastUsed, expires, revokedAt}}
}
