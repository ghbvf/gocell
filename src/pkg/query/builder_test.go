package query

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuilder_SimpleQuery(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM users WHERE")
	b.AppendParam("id = ", 42)

	sql, args := b.Build()
	assert.Equal(t, "SELECT * FROM users WHERE id = $1", sql)
	assert.Equal(t, []any{42}, args)
}

func TestBuilder_MultipleParams(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT * FROM outbox_entries WHERE")
	b.AppendParam("aggregate_type = ", "order")
	b.Append("AND")
	b.AppendParam("published = ", false)

	sql, args := b.Build()
	assert.Equal(t, "SELECT * FROM outbox_entries WHERE aggregate_type = $1 AND published = $2", sql)
	assert.Equal(t, []any{"order", false}, args)
}

func TestBuilder_AppendIf(t *testing.T) {
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
			b := NewBuilder()
			b.Append("SELECT * FROM t")
			b.AppendIf(tt.condition, "WHERE status = ", "active")

			sql, args := b.Build()
			assert.Equal(t, tt.wantSQL, sql)
			assert.Len(t, args, tt.wantArgs)
		})
	}
}

func TestBuilder_NextParam(t *testing.T) {
	b := NewBuilder()
	p1 := b.NextParam("value1")
	p2 := b.NextParam("value2")

	assert.Equal(t, "$1", p1)
	assert.Equal(t, "$2", p2)
	assert.Equal(t, []any{"value1", "value2"}, b.Args())
}

func TestBuilder_Reset(t *testing.T) {
	b := NewBuilder()
	b.Append("SELECT 1")
	b.AppendParam("WHERE x = ", 1)

	b.Reset()
	sql, args := b.Build()
	assert.Equal(t, "", sql)
	assert.Empty(t, args)

	// Reuse after reset.
	b.Append("SELECT 2")
	sql, args = b.Build()
	assert.Equal(t, "SELECT 2", sql)
	assert.Empty(t, args)
}

func TestBuilder_SQL(t *testing.T) {
	b := NewBuilder()
	b.Append("INSERT INTO t (a, b) VALUES")
	b.AppendParam("(", 1)
	// SQL returns just the text parts.
	assert.Contains(t, b.SQL(), "INSERT INTO t")
}

func TestBuilder_Empty(t *testing.T) {
	b := NewBuilder()
	sql, args := b.Build()
	assert.Equal(t, "", sql)
	assert.Empty(t, args)
}

func TestBuilder_ComplexQuery(t *testing.T) {
	b := NewBuilder()
	b.Append("UPDATE outbox_entries SET published = true,")
	b.AppendParam("published_at = ", "2024-01-01T00:00:00Z")
	b.Append("WHERE")
	b.AppendParam("id = ", "uuid-123")
	b.Append("AND published = false")

	sql, args := b.Build()
	expected := "UPDATE outbox_entries SET published = true, published_at = $1 WHERE id = $2 AND published = false"
	assert.Equal(t, expected, sql)
	assert.Equal(t, []any{"2024-01-01T00:00:00Z", "uuid-123"}, args)
}
