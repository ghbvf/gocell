package query

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey() []byte { return bytes.Repeat([]byte("k"), 32) }

func TestCursorCodec_NewRequiresMinKeyLength(t *testing.T) {
	tests := []struct {
		name    string
		keyLen  int
		wantErr bool
	}{
		{"empty", 0, true},
		{"too short 16", 16, true},
		{"too short 31", 31, true},
		{"exact minimum 32", 32, false},
		{"longer 64", 64, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := bytes.Repeat([]byte("x"), tt.keyLen)
			codec, err := NewCursorCodec(key)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, codec)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, codec)
			}
		})
	}
}

func TestCursorCodec_RoundTrip(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	cur := Cursor{Values: []any{"hello", float64(42)}}
	token, err := codec.Encode(cur)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	decoded, err := codec.Decode(token)
	require.NoError(t, err)
	assert.Equal(t, cur.Values, decoded.Values)
}

func TestCursorCodec_RoundTrip_TimeAsString(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	cur := Cursor{Values: []any{"2026-04-12T10:30:00Z", "id-123"}}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	decoded, err := codec.Decode(token)
	require.NoError(t, err)
	assert.Equal(t, "2026-04-12T10:30:00Z", decoded.Values[0])
	assert.Equal(t, "id-123", decoded.Values[1])
}

func TestCursorCodec_RoundTrip_NumericTypes(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	cur := Cursor{Values: []any{float64(100), float64(3.14)}}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	decoded, err := codec.Decode(token)
	require.NoError(t, err)
	assert.Equal(t, float64(100), decoded.Values[0])
	assert.InDelta(t, 3.14, decoded.Values[1].(float64), 0.001)
}

func TestCursorCodec_Decode_TamperedPayload(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	token, err := codec.Encode(Cursor{Values: []any{"original"}})
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	raw[0] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	_, err = codec.Decode(tampered)
	assert.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestCursorCodec_Decode_TamperedSignature(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	token, err := codec.Encode(Cursor{Values: []any{"data"}})
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	dotIdx := bytes.LastIndexByte(raw, '.')
	require.Greater(t, dotIdx, 0)
	raw[dotIdx+1] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	_, err = codec.Decode(tampered)
	assert.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestCursorCodec_Decode_InvalidBase64(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	_, err = codec.Decode("not-valid-base64!!!")
	assert.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestCursorCodec_Decode_EmptyToken(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	_, err = codec.Decode("")
	assert.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestCursorCodec_DifferentKeysReject(t *testing.T) {
	codecA, err := NewCursorCodec(bytes.Repeat([]byte("a"), 32))
	require.NoError(t, err)
	codecB, err := NewCursorCodec(bytes.Repeat([]byte("b"), 32))
	require.NoError(t, err)

	token, err := codecA.Encode(Cursor{Values: []any{"secret"}})
	require.NoError(t, err)

	_, err = codecB.Decode(token)
	assert.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestCursorCodec_RoundTrip_EmptyValues(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	cur := Cursor{Values: []any{}}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	decoded, err := codec.Decode(token)
	require.NoError(t, err)
	assert.Empty(t, decoded.Values)
}

func TestCursorCodec_RoundTrip_DotInPayload(t *testing.T) {
	// Verify that '.' characters in cursor values don't confuse
	// the LastIndexByte separator logic (signature hex never contains '.').
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	cur := Cursor{Values: []any{"domain.name.v1", "event.type.created"}}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	decoded, err := codec.Decode(token)
	require.NoError(t, err)
	assert.Equal(t, "domain.name.v1", decoded.Values[0])
	assert.Equal(t, "event.type.created", decoded.Values[1])
}

func TestCursorCodec_CrossCellRejection(t *testing.T) {
	// Cursors signed by one cell's codec must be rejected by a different cell's codec.
	// This validates the per-cell unique demo key isolation.
	cellA, err := NewCursorCodec([]byte("gocell-demo-ORDER-CELL-key-32b!!"))
	require.NoError(t, err)
	cellB, err := NewCursorCodec([]byte("gocell-demo-CONFIG-CORE-key-32!!"))
	require.NoError(t, err)

	token, err := cellA.Encode(Cursor{Values: []any{"val"}, Scope: "scope-a"})
	require.NoError(t, err)

	_, err = cellB.Decode(token)
	assert.Error(t, err)
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

// --- SortScope tests ---

func TestSortScope_Deterministic(t *testing.T) {
	cols := []SortColumn{
		{Name: "created_at", Direction: SortDESC},
		{Name: "id", Direction: SortASC},
	}
	assert.Equal(t, SortScope(cols), SortScope(cols))
	assert.Len(t, SortScope(cols), 16)
}

func TestSortScope_DifferentColumnsProduceDifferentScope(t *testing.T) {
	a := []SortColumn{{Name: "created_at", Direction: SortDESC}, {Name: "id", Direction: SortASC}}
	b := []SortColumn{{Name: "key", Direction: SortASC}, {Name: "id", Direction: SortASC}}
	assert.NotEqual(t, SortScope(a), SortScope(b))
}

// --- ValidateCursorScope tests ---

func TestValidateCursorScope_Mismatch(t *testing.T) {
	sortA := []SortColumn{{Name: "created_at", Direction: SortDESC}, {Name: "id", Direction: SortASC}}
	sortB := []SortColumn{{Name: "key", Direction: SortASC}, {Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1", "v2"}, Scope: SortScope(sortA), Context: qctx}
	err := ValidateCursorScope(cur, sortB, qctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "scope mismatch")
}

func TestValidateCursorScope_ValueCountMismatch(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1", "v2"}, Scope: SortScope(sort), Context: qctx}
	err := ValidateCursorScope(cur, sort, qctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected 1")
}

func TestValidateCursorScope_Valid(t *testing.T) {
	sort := []SortColumn{{Name: "created_at", Direction: SortDESC}, {Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"2026-01-01T00:00:00Z", "id-1"}, Scope: SortScope(sort), Context: qctx}
	assert.NoError(t, ValidateCursorScope(cur, sort, qctx))
}

func TestValidateCursorScope_MissingScope(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1"}, Context: qctx} // Scope intentionally empty
	err := ValidateCursorScope(cur, sort, qctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "scope mismatch")
}

func TestValidateCursorScope_MissingContext(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1"}, Scope: SortScope(sort)} // Context intentionally empty
	err := ValidateCursorScope(cur, sort, qctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context mismatch")
}

func TestValidateCursorScope_ContextMismatch(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	ctxA := QueryContext("endpoint", "orders")
	ctxB := QueryContext("endpoint", "configs")
	cur := Cursor{Values: []any{"v1"}, Scope: SortScope(sort), Context: ctxA}
	err := ValidateCursorScope(cur, sort, ctxB)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context mismatch")
}

func TestValidateCursorScope_ContextMatch(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "orders")
	cur := Cursor{Values: []any{"v1"}, Scope: SortScope(sort), Context: qctx}
	assert.NoError(t, ValidateCursorScope(cur, sort, qctx))
}

func TestQueryContext_Deterministic(t *testing.T) {
	a := QueryContext("endpoint", "audit-query", "eventType", "login")
	b := QueryContext("endpoint", "audit-query", "eventType", "login")
	assert.Equal(t, a, b)
	assert.Len(t, a, 16)
}

func TestQueryContext_DifferentValues(t *testing.T) {
	a := QueryContext("endpoint", "audit-query", "eventType", "login")
	b := QueryContext("endpoint", "audit-query", "eventType", "logout")
	assert.NotEqual(t, a, b)
}

// --- Key rotation tests ---

func TestCursorCodec_KeyRotation(t *testing.T) {
	keyOld := bytes.Repeat([]byte("o"), 32)
	keyNew := bytes.Repeat([]byte("n"), 32)

	// Sign with old key.
	codecOld, err := NewCursorCodec(keyOld)
	require.NoError(t, err)
	token, err := codecOld.Encode(Cursor{Values: []any{"val"}})
	require.NoError(t, err)

	// New codec with key rotation: current=new, previous=old.
	codecRotated, err := NewCursorCodec(keyNew, keyOld)
	require.NoError(t, err)

	// Should verify with the previous key.
	decoded, err := codecRotated.Decode(token)
	require.NoError(t, err)
	assert.Equal(t, []any{"val"}, decoded.Values)

	// Encode with new codec uses new key.
	newToken, err := codecRotated.Encode(Cursor{Values: []any{"new-val"}})
	require.NoError(t, err)

	// Old codec can't verify new token.
	_, err = codecOld.Decode(newToken)
	assert.Error(t, err)
}

func TestCursorCodec_NewRequiresPreviousKeyMinLength(t *testing.T) {
	current := bytes.Repeat([]byte("c"), 32)
	shortPrev := bytes.Repeat([]byte("p"), 16)

	_, err := NewCursorCodec(current, shortPrev)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "previous cursor HMAC key")
}

func TestCursorCodec_RoundTrip_WithScope(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	sort := []SortColumn{{Name: "created_at", Direction: SortDESC}, {Name: "id", Direction: SortASC}}
	cur := Cursor{Values: []any{"2026-01-01T00:00:00Z", "id-1"}, Scope: SortScope(sort)}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	decoded, err := codec.Decode(token)
	require.NoError(t, err)
	assert.Equal(t, cur.Scope, decoded.Scope)
	assert.Equal(t, cur.Values, decoded.Values)
}
