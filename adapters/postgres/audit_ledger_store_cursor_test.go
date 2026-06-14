package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// TestBindTimestampCursor exercises bindTimestampCursor in isolation — the
// helper that converts the RFC3339Nano string timestamp cursor value back to
// time.Time before the pgx timestamptz keyset bind. The conformance suite only
// covers the happy path; this locks the nil / non-string / invalid-string /
// length-guard branches that the SQL path never reaches. No DB required.
func TestBindTimestampCursor(t *testing.T) {
	const tsStr = "2025-01-02T03:04:05.123456789Z"
	wantTS, err := time.Parse(time.RFC3339Nano, tsStr)
	require.NoError(t, err)

	t.Run("nil cursor returns params unchanged (first page)", func(t *testing.T) {
		in := query.ListParams{Limit: 10, Sort: ledger.QuerySort()}
		out, err := bindTimestampCursor(in)
		require.NoError(t, err)
		require.Nil(t, out.CursorValues)
	})

	t.Run("valid RFC3339Nano timestamp converted to time.Time, id untouched", func(t *testing.T) {
		in := query.ListParams{
			Limit:        10,
			Sort:         ledger.QuerySort(),
			CursorValues: []any{tsStr, "id-7"},
		}
		out, err := bindTimestampCursor(in)
		require.NoError(t, err)

		ts, ok := out.CursorValues[0].(time.Time)
		require.True(t, ok, "timestamp cursor value must be converted to time.Time, got %T", out.CursorValues[0])
		require.True(t, ts.Equal(wantTS), "converted timestamp mismatch: got %v want %v", ts, wantTS)
		require.Equal(t, "id-7", out.CursorValues[1], "id UUID tie-breaker must be left untouched")

		// Caller's slice must not be mutated (slices.Clone defensiveness).
		require.Equal(t, tsStr, in.CursorValues[0], "input CursorValues must not be mutated")
	})

	t.Run("invalid timestamp string returns ErrCursorInvalid", func(t *testing.T) {
		in := query.ListParams{
			Limit:        10,
			Sort:         ledger.QuerySort(),
			CursorValues: []any{"not-a-timestamp", "id-7"},
		}
		_, err := bindTimestampCursor(in)
		var coded *errcode.Error
		require.True(t, errors.As(err, &coded), "expected *errcode.Error, got %T: %v", err, err)
		require.Equal(t, errcode.ErrCursorInvalid, coded.Code)
	})

	t.Run("non-string timestamp value left untouched", func(t *testing.T) {
		// A time.Time already in place (defensive: should not be re-parsed).
		in := query.ListParams{
			Limit:        10,
			Sort:         ledger.QuerySort(),
			CursorValues: []any{wantTS, "id-7"},
		}
		out, err := bindTimestampCursor(in)
		require.NoError(t, err)
		require.Equal(t, wantTS, out.CursorValues[0])
	})

	t.Run("cursor shorter than sort does not panic", func(t *testing.T) {
		in := query.ListParams{
			Limit:        10,
			Sort:         ledger.QuerySort(), // 2 columns
			CursorValues: []any{tsStr},       // 1 value
		}
		out, err := bindTimestampCursor(in)
		require.NoError(t, err)
		_, ok := out.CursorValues[0].(time.Time)
		require.True(t, ok)
	})
}
