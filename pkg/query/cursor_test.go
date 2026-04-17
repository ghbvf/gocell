package query

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

func TestCursorCodec_Encode_MarshalFailure(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	// Cursor.Values is []any — a func value is not JSON-serializable.
	// Encode failure is a server-side error (ErrInternal, not ErrCursorInvalid)
	// because the client sent no cursor; the server failed to build nextCursor.
	cur := Cursor{Values: []any{func() {}}}
	_, err = codec.Encode(cur)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)
	assert.Equal(t, "cursor: marshal failed", ecErr.Message)
	assert.NotNil(t, ecErr.Cause, "must preserve underlying json.Marshal error for diagnosis")
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
	requireCursorInvalid(t, err, "signature verification failed")
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
	requireCursorInvalid(t, err, "signature verification failed")
}

func TestCursorCodec_Decode_InvalidBase64(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	_, err = codec.Decode("not-valid-base64!!!")
	requireCursorInvalid(t, err, "invalid base64 encoding")
}

func TestCursorCodec_Decode_MissingSeparator(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	// Valid base64 but no '.' separator inside.
	raw := []byte("nodothere")
	token := base64.RawURLEncoding.EncodeToString(raw)

	_, err = codec.Decode(token)
	requireCursorInvalid(t, err, "missing signature separator")
}

func TestCursorCodec_Decode_InvalidPayload(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	// Construct a properly signed token with invalid JSON payload.
	payload := []byte("not-valid-json{{{")
	mac := hmac.New(sha256.New, testKey())
	mac.Write(payload)
	sum := mac.Sum(nil)
	sig := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(sig, sum)

	raw := make([]byte, 0, len(payload)+1+len(sig))
	raw = append(raw, payload...)
	raw = append(raw, '.')
	raw = append(raw, sig...)
	token := base64.RawURLEncoding.EncodeToString(raw)

	_, err = codec.Decode(token)
	requireCursorInvalid(t, err, "invalid payload")
}

func TestCursorCodec_Decode_EmptyToken(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	_, err = codec.Decode("")
	requireCursorInvalid(t, err, "cursor token is empty")
}

func TestCursorCodec_DifferentKeysReject(t *testing.T) {
	codecA, err := NewCursorCodec(bytes.Repeat([]byte("a"), 32))
	require.NoError(t, err)
	codecB, err := NewCursorCodec(bytes.Repeat([]byte("b"), 32))
	require.NoError(t, err)

	token, err := codecA.Encode(Cursor{Values: []any{"secret"}})
	require.NoError(t, err)

	_, err = codecB.Decode(token)
	requireCursorInvalid(t, err, "signature verification failed")
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
	requireCursorInvalid(t, err, "signature verification failed")
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

// requireCursorInvalid asserts err is *errcode.Error with ErrCursorInvalid code
// and the expected reason in Details. This catches regressions where the error
// type or code drifts while the message text still matches.
func requireCursorInvalid(t *testing.T, err error, wantReason string) {
	t.Helper()
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, cursorInvalidMsg, ecErr.Message)
	assert.Equal(t, wantReason, ecErr.Details["reason"])
}

func TestValidateCursorScope_Mismatch(t *testing.T) {
	sortA := []SortColumn{{Name: "created_at", Direction: SortDESC}, {Name: "id", Direction: SortASC}}
	sortB := []SortColumn{{Name: "key", Direction: SortASC}, {Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1", "v2"}, Scope: SortScope(sortA), Context: qctx}
	err := ValidateCursorScope(cur, sortB, qctx)
	requireCursorInvalid(t, err, "sort scope mismatch")

	// Assert got/want diagnostics from cursorInvalidExtra.
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, SortScope(sortA), ecErr.Details["got"])
	assert.Equal(t, SortScope(sortB), ecErr.Details["want"])
}

func TestValidateCursorScope_ValueCountMismatch(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1", "v2"}, Scope: SortScope(sort), Context: qctx}
	err := ValidateCursorScope(cur, sort, qctx)
	requireCursorInvalid(t, err, "has 2 values but expected 1 sort columns")
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
	requireCursorInvalid(t, err, "sort scope is required")
}

func TestValidateCursorScope_MissingContext(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1"}, Scope: SortScope(sort)} // Context intentionally empty
	err := ValidateCursorScope(cur, sort, qctx)
	requireCursorInvalid(t, err, "query context is required")
}

func TestValidateCursorScope_BothMissing(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	qctx := QueryContext("endpoint", "test")
	cur := Cursor{Values: []any{"v1"}} // both Scope and Context empty
	err := ValidateCursorScope(cur, sort, qctx)
	requireCursorInvalid(t, err, "sort scope is required") // scope check fires first
}

func TestValidateCursorScope_ContextMismatch(t *testing.T) {
	sort := []SortColumn{{Name: "id", Direction: SortASC}}
	ctxA := QueryContext("endpoint", "orders")
	ctxB := QueryContext("endpoint", "configs")
	cur := Cursor{Values: []any{"v1"}, Scope: SortScope(sort), Context: ctxA}
	err := ValidateCursorScope(cur, sort, ctxB)
	requireCursorInvalid(t, err, "query context mismatch")

	// Assert got/want diagnostics from cursorInvalidExtra.
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, ctxA, ecErr.Details["got"])
	assert.Equal(t, ctxB, ecErr.Details["want"])
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

// TestCursorCodec_New_PreviousEqualsCurrent_Rejected asserts that NewCursorCodec
// rejects a previous key that is identical to the current key. Using the same
// value for both degrades rotation to a no-op and is a likely operator mistake.
func TestCursorCodec_New_PreviousEqualsCurrent_Rejected(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	_, err := NewCursorCodec(key, key)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Contains(t, err.Error(), "previous cursor key must differ from current")
}

// TestCursorCodec_PreviousKeyNilVsEmpty confirms that nil and []byte{} previous
// keys both disable rotation without error (API regression guard). Matches
// gorilla/securecookie CodecsFromPairs treatment of empty key slots.
func TestCursorCodec_PreviousKeyNilVsEmpty(t *testing.T) {
	current := bytes.Repeat([]byte("c"), 32)

	codecNil, err := NewCursorCodec(current)
	require.NoError(t, err)
	require.NotNil(t, codecNil)

	codecEmpty, err := NewCursorCodec(current, []byte{})
	require.NoError(t, err)
	require.NotNil(t, codecEmpty)

	tok, err := codecNil.Encode(Cursor{Values: []any{"x"}})
	require.NoError(t, err)
	decoded, err := codecEmpty.Decode(tok)
	require.NoError(t, err)
	assert.Equal(t, []any{"x"}, decoded.Values)
}

// TestCursorCodec_AllKeysFail_NoSideChannel asserts that when both current and
// previous keys fail signature verification, the returned error does NOT leak
// information about which key was "closer" or mention keys at all in the
// client-facing details. Required defense against key-identification side
// channels.
// ref: gorilla/securecookie MultiError aggregates but exposes no per-key index.
func TestCursorCodec_AllKeysFail_NoSideChannel(t *testing.T) {
	keyA := bytes.Repeat([]byte("a"), 32)
	keyB := bytes.Repeat([]byte("b"), 32)
	keyC := bytes.Repeat([]byte("c"), 32)

	codecSign, err := NewCursorCodec(keyA)
	require.NoError(t, err)
	codecVerify, err := NewCursorCodec(keyB, keyC)
	require.NoError(t, err)

	token, err := codecSign.Encode(Cursor{Values: []any{"v"}})
	require.NoError(t, err)

	_, err = codecVerify.Decode(token)
	requireCursorInvalid(t, err, "signature verification failed")

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	for k, v := range ecErr.Details {
		assert.NotContains(t, k, "key", "details key must not reveal key role")
		assert.NotContains(t, k, "current", "details must not expose rotation position")
		assert.NotContains(t, k, "previous", "details must not expose rotation position")
		if s, ok := v.(string); ok {
			assert.NotContains(t, s, "current", "detail value must not expose rotation position")
			assert.NotContains(t, s, "previous", "detail value must not expose rotation position")
		}
	}
	// InternalMessage is the server-side diagnostic; it also must not leak rotation position.
	assert.NotContains(t, ecErr.InternalMessage, "current",
		"InternalMessage must not expose rotation position")
	assert.NotContains(t, ecErr.InternalMessage, "previous",
		"InternalMessage must not expose rotation position")
}

// TestCursorCodec_KeyRotation_Lifecycle3Step is a regression guard on the
// 3-step rotation lifecycle: tokens signed by the old key continue to verify
// after the operator promotes a new current key and keeps old as previous.
// ref: kube-apiserver --service-account-key-file 3-step rotation.
func TestCursorCodec_KeyRotation_Lifecycle3Step(t *testing.T) {
	keyOld := bytes.Repeat([]byte("o"), 32)
	keyNew := bytes.Repeat([]byte("n"), 32)

	codecOld, err := NewCursorCodec(keyOld)
	require.NoError(t, err)
	tokenOld, err := codecOld.Encode(Cursor{Values: []any{"legacy"}})
	require.NoError(t, err)

	codecRotated, err := NewCursorCodec(keyNew, keyOld)
	require.NoError(t, err)

	decoded, err := codecRotated.Decode(tokenOld)
	require.NoError(t, err)
	assert.Equal(t, []any{"legacy"}, decoded.Values)

	tokenNew, err := codecRotated.Encode(Cursor{Values: []any{"fresh"}})
	require.NoError(t, err)

	// Step 3 simulation: keyOld removed, keyNew-only codec verifies tokens
	// written during the rotation window, but legacy tokens fail — this is
	// the invariant the 3-step process protects (operator waits TTL).
	codecNewOnly, err := NewCursorCodec(keyNew)
	require.NoError(t, err)
	decoded, err = codecNewOnly.Decode(tokenNew)
	require.NoError(t, err)
	assert.Equal(t, []any{"fresh"}, decoded.Values)

	_, err = codecNewOnly.Decode(tokenOld)
	requireCursorInvalid(t, err, "signature verification failed")
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

// TestCursorCodec_Decode_TooLong rejects oversize cursor tokens to bound
// decode work. Without an upper bound an attacker can force unbounded
// base64 + HMAC work by supplying megabyte-sized cursors.
// ref: kubernetes apiserver 4 KiB continue-token guidance.
func TestCursorCodec_Decode_TooLong(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	oversize := make([]byte, MaxCursorTokenBytes+1)
	for i := range oversize {
		oversize[i] = 'A'
	}
	_, err = codec.Decode(string(oversize))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "cursor token exceeds maximum length", ecErr.Details["reason"])
}

// TestCursorCodec_Decode_MaxLengthBoundary confirms the exact-limit cursor
// is not rejected by the length guard (only the signature check fails).
func TestCursorCodec_Decode_MaxLengthBoundary(t *testing.T) {
	codec, err := NewCursorCodec(testKey())
	require.NoError(t, err)

	atLimit := make([]byte, MaxCursorTokenBytes)
	for i := range atLimit {
		atLimit[i] = 'A'
	}
	_, err = codec.Decode(string(atLimit))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	// Must not be rejected by the length guard.
	assert.NotEqual(t, "cursor token exceeds maximum length", ecErr.Details["reason"])
}
