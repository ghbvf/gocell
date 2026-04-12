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
