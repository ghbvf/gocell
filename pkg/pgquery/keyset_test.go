package pgquery

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestKeyset_OrderBy_Single(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "created_at", Direction: query.SortDESC}},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, _ := b.Build()
	assert.Contains(t, sql, "ORDER BY created_at DESC")
}

func TestKeyset_OrderBy_Multi(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit: 10,
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, _ := b.Build()
	assert.Contains(t, sql, "ORDER BY created_at DESC, id ASC")
}

func TestKeyset_Where_NoCursor(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        10,
		CursorValues: nil,
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.NotContains(t, sql, "AND (")
	assert.Contains(t, sql, "ORDER BY id ASC")
	assert.Contains(t, sql, "LIMIT")
	assert.Len(t, args, 1)
}

func TestKeyset_Where_SingleColumn_ASC(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"abc"},
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.Contains(t, sql, "AND id > $1")
	assert.Equal(t, "abc", args[0])
}

func TestKeyset_Where_SingleColumn_DESC(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"2026-01-01T00:00:00Z"},
		Sort:         []query.SortColumn{{Name: "created_at", Direction: query.SortDESC}},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.Contains(t, sql, "AND created_at < $1")
	assert.Equal(t, "2026-01-01T00:00:00Z", args[0])
}

func TestKeyset_Where_SameDir_Tuple(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"2026-01-01T00:00:00Z", "id-99"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortDESC},
		},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.Contains(t, sql, "AND (created_at, id) < ($1, $2)")
	assert.Equal(t, "2026-01-01T00:00:00Z", args[0])
	assert.Equal(t, "id-99", args[1])
}

func TestKeyset_Where_SameDir_ASC_Tuple(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"alpha", "id-01"},
		Sort: []query.SortColumn{
			{Name: "key", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.Contains(t, sql, "AND (key, id) > ($1, $2)")
	assert.Equal(t, "alpha", args[0])
	assert.Equal(t, "id-01", args[1])
}

func TestKeyset_Where_MixedDir_CompoundOR(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"2026-01-01T00:00:00Z", "id-42"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.Contains(t, sql, "AND (created_at < $1 OR (created_at = $2 AND id > $3))")
	assert.Len(t, args, 4)
	assert.Equal(t, "2026-01-01T00:00:00Z", args[0])
	assert.Equal(t, "2026-01-01T00:00:00Z", args[1])
	assert.Equal(t, "id-42", args[2])
}

func TestKeyset_Where_ThreeColumns_Mixed(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        5,
		CursorValues: []any{"a", "b", "c"},
		Sort: []query.SortColumn{
			{Name: "x", Direction: query.SortDESC},
			{Name: "y", Direction: query.SortASC},
			{Name: "z", Direction: query.SortDESC},
		},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.Contains(t, sql, "AND (x < $1 OR (x = $2 AND y > $3) OR (x = $4 AND y = $5 AND z < $6))")
	assert.Len(t, args, 7)
}

func TestKeyset_IntegratesWithExistingWhere(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM orders WHERE 1=1")
	b.AppendParam("AND status = ", "active")
	params := query.ListParams{
		Limit:        20,
		CursorValues: []any{"id-5"},
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}
	require.NoError(t, AppendKeyset(b, params))
	sql, args := b.Build()
	assert.Contains(t, sql, "AND status = $1")
	assert.Contains(t, sql, "AND id > $2")
	assert.Equal(t, "active", args[0])
	assert.Equal(t, "id-5", args[1])
}

func TestKeyset_SetsLimitPlusOne(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit: 25,
		Sort:  []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}
	require.NoError(t, AppendKeyset(b, params))
	_, args := b.Build()
	assert.Equal(t, 26, args[len(args)-1])
}

func TestKeyset_CursorValueCountMismatch(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"only-one"},
		Sort: []query.SortColumn{
			{Name: "a", Direction: query.SortASC},
			{Name: "b", Direction: query.SortASC},
		},
	}
	err := AppendKeyset(b, params)
	requireCursorInvalid(t, err, "cursor has 1 values but 2 sort columns")
}

func TestKeyset_FullQuery(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT id, name, created_at FROM users WHERE 1=1")
	b.AppendParam("AND role = ", "admin")
	b.AppendIf(true, "AND active = ", true)

	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"2026-01-01T00:00:00Z", "id-100"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	require.NoError(t, AppendKeyset(b, params))

	sql, args := b.Build()
	assert.Contains(t, sql, "AND role = $1")
	assert.Contains(t, sql, "AND active = $2")
	assert.Contains(t, sql, "AND (created_at < $3 OR (created_at = $4 AND id > $5))")
	assert.Contains(t, sql, "ORDER BY created_at DESC, id ASC")
	assert.Contains(t, sql, "LIMIT $6")
	assert.Len(t, args, 6)
	assert.Equal(t, 11, args[5])
}

func TestKeyset_EmptySort(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t")
	params := query.ListParams{Limit: 10, Sort: nil}
	err := AppendKeyset(b, params)
	assert.Error(t, err)
}

func TestKeyset_InvalidColumnName(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "Robert'; DROP TABLE students;--", Direction: query.SortASC}},
	}
	err := AppendKeyset(b, params)
	assert.Error(t, err)
	var ecErrCol *errcode.Error
	require.True(t, errors.As(err, &ecErrCol))
	assert.Contains(t, ecErrCol.Message+" "+ecErrCol.InternalMessage, "invalid column name")
}

func TestKeyset_InvalidDirection(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM t WHERE 1=1")
	params := query.ListParams{
		Limit: 10,
		Sort:  []query.SortColumn{{Name: "id", Direction: query.SortDir("RANDOM")}},
	}
	err := AppendKeyset(b, params)
	assert.Error(t, err)
	var ecErrDir *errcode.Error
	require.True(t, errors.As(err, &ecErrDir))
	assert.Contains(t, ecErrDir.Message+" "+ecErrDir.InternalMessage, "invalid sort direction")
}

func requireCursorInvalid(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)

	var got *errcode.Error
	require.ErrorAs(t, err, &got)
	assert.Equal(t, errcode.ErrCursorInvalid, got.Code)
	reasonAttr, ok := got.FindAttr("reason")
	require.True(t, ok)
	assert.Equal(t, reason, reasonAttr.Value.String())
}
