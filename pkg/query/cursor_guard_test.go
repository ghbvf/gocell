package query

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDemoKey_KnownDemoKey(t *testing.T) {
	codec, err := NewCursorCodec([]byte("gocell-demo-AUDIT--CORE-key-32!!"))
	require.NoError(t, err)
	assert.True(t, codec.IsDemoKey(KnownDemoKeys()...))
}

func TestIsDemoKey_ProductionKey(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	codec, err := NewCursorCodec(key)
	require.NoError(t, err)
	assert.False(t, codec.IsDemoKey(KnownDemoKeys()...))
}

func TestIsDemoKey_EmptyKnownList(t *testing.T) {
	codec, err := NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	require.NoError(t, err)
	assert.False(t, codec.IsDemoKey())
}

func TestIsDemoKey_CurrentIsDemo_WithRotation(t *testing.T) {
	prodKey := make([]byte, 32)
	_, _ = rand.Read(prodKey)
	demoKey := []byte("gocell-demo-CONFIG-CORE-key-32!!")

	codec, err := NewCursorCodec(demoKey, prodKey)
	require.NoError(t, err)
	assert.True(t, codec.IsDemoKey(KnownDemoKeys()...))
}

func TestIsDemoKey_PreviousIsDemo_CurrentIsProd(t *testing.T) {
	prodKey := make([]byte, 32)
	_, _ = rand.Read(prodKey)
	demoKey := []byte("gocell-demo-CONFIG-CORE-key-32!!")

	codec, err := NewCursorCodec(prodKey, demoKey)
	require.NoError(t, err)
	assert.False(t, codec.IsDemoKey(KnownDemoKeys()...))
}
