package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQueryBuilder_SimpleQuery(t *testing.T) {
	qb := NewQueryBuilder()
	qb.Append("SELECT * FROM users WHERE")
	qb.AppendParam("id = ", 42)

	sql, args := qb.Build()
	assert.Equal(t, "SELECT * FROM users WHERE id = $1", sql)
	assert.Equal(t, []any{42}, args)
}

func TestQueryBuilder_MultipleParams(t *testing.T) {
	qb := NewQueryBuilder()
	qb.Append("SELECT * FROM outbox_entries WHERE")
	qb.AppendParam("aggregate_type = ", "order")
	qb.Append("AND")
	qb.AppendParam("published = ", false)

	sql, args := qb.Build()
	assert.Equal(t, "SELECT * FROM outbox_entries WHERE aggregate_type = $1 AND published = $2", sql)
	assert.Equal(t, []any{"order", false}, args)
}

func TestQueryBuilder_AppendIf(t *testing.T) {
	tests := []struct {
		name      string
		condition bool
		wantSQL   string
		wantArgs  int
	}{
		{
			name:      "condition true",
			condition: true,
			wantSQL:   "SELECT * FROM t WHERE status = $1",
			wantArgs:  1,
		},
		{
			name:      "condition false",
			condition: false,
			wantSQL:   "SELECT * FROM t",
			wantArgs:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder()
			qb.Append("SELECT * FROM t")
			qb.AppendIf(tt.condition, "WHERE status = ", "active")

			sql, args := qb.Build()
			assert.Equal(t, tt.wantSQL, sql)
			assert.Len(t, args, tt.wantArgs)
		})
	}
}

func TestQueryBuilder_NextParam(t *testing.T) {
	qb := NewQueryBuilder()
	p1 := qb.NextParam("value1")
	p2 := qb.NextParam("value2")

	assert.Equal(t, "$1", p1)
	assert.Equal(t, "$2", p2)
	assert.Equal(t, []any{"value1", "value2"}, qb.Args())
}

func TestQueryBuilder_Reset(t *testing.T) {
	qb := NewQueryBuilder()
	qb.Append("SELECT 1")
	qb.AppendParam("WHERE x = ", 1)

	qb.Reset()
	sql, args := qb.Build()
	assert.Equal(t, "", sql)
	assert.Empty(t, args)

	// Reuse after reset.
	qb.Append("SELECT 2")
	sql, args = qb.Build()
	assert.Equal(t, "SELECT 2", sql)
	assert.Empty(t, args)
}

func TestQueryBuilder_SQL(t *testing.T) {
	qb := NewQueryBuilder()
	qb.Append("INSERT INTO t (a, b) VALUES")
	qb.AppendParam("(", 1)
	// SQL returns just the text parts.
	assert.Contains(t, qb.SQL(), "INSERT INTO t")
}

func TestQueryBuilder_Empty(t *testing.T) {
	qb := NewQueryBuilder()
	sql, args := qb.Build()
	assert.Equal(t, "", sql)
	assert.Empty(t, args)
}

func TestQueryBuilder_ComplexQuery(t *testing.T) {
	qb := NewQueryBuilder()
	qb.Append("UPDATE outbox_entries SET published = true,")
	qb.AppendParam("published_at = ", "2024-01-01T00:00:00Z")
	qb.Append("WHERE")
	qb.AppendParam("id = ", "uuid-123")
	qb.Append("AND published = false")

	sql, args := qb.Build()
	expected := "UPDATE outbox_entries SET published = true, published_at = $1 WHERE id = $2 AND published = false"
	assert.Equal(t, expected, sql)
	assert.Equal(t, []any{"2024-01-01T00:00:00Z", "uuid-123"}, args)
}
