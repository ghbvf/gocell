package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// TestBuildCursorCodec_DevDefault_Succeeds confirms that in dev mode (empty
// adapterMode) the dev default secret produces a usable codec when primary is
// empty, matching the dev-fallback contract.
func TestBuildCursorCodec_DevDefault_Succeeds(t *testing.T) {
	codec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     "",
		Previous:    "",
		DevDefault:  "corebundle-audit-cursor-key-32b!",
		Label:       "audit",
	})
	require.NoError(t, err)
	require.NotNil(t, codec)

	_, err = codec.Encode(query.Cursor{Values: []any{"x"}})
	require.NoError(t, err, "dev default codec must round-trip")
}

// TestBuildCursorCodec_RealModePrimaryEmpty_FailFast asserts that in adapter
// mode "real" an empty primary triggers the hard-error branch, and the error
// preserves the env-label for operator diagnosis.
func TestBuildCursorCodec_RealModePrimaryEmpty_FailFast(t *testing.T) {
	codec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     "",
		Previous:    "",
		DevDefault:  "corebundle-audit-cursor-key-32b!",
		Label:       "audit",
	})
	require.Error(t, err)
	assert.Nil(t, codec)
	assert.Contains(t, err.Error(), "GOCELL_TEST_CURSOR_KEY",
		"error must name the missing env var")
	assert.Contains(t, err.Error(), "audit", "error must preserve label")
	assert.Contains(t, err.Error(), "adapter mode \"real\"",
		"error must indicate the mode that triggered fail-fast")
}

// TestBuildCursorCodec_DemoKeyInRealMode_Rejected asserts that even when
// primary is non-empty, a well-known demo value is rejected in real mode.
// Reachable if an operator copies the dev default into prod config.
func TestBuildCursorCodec_DemoKeyInRealMode_Rejected(t *testing.T) {
	_, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     "corebundle-audit-cursor-key-32b!",
		Previous:    "",
		DevDefault:  "corebundle-audit-cursor-key-32b!",
		Label:       "audit",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "well-known demo key",
		"error must come from rejectDemoKey")
	assert.Contains(t, err.Error(), "GOCELL_TEST_CURSOR_KEY")
}

// TestBuildCursorCodec_WithPreviousKey_Rotation wires current+previous keys
// and confirms the resulting codec verifies tokens signed by either key — the
// core invariant of the 3-step rotation lifecycle.
// ref: kube-apiserver --service-account-key-file (verification) +
//
//	--service-account-signing-key-file (signing) — two slots, decode tries all.
func TestBuildCursorCodec_WithPreviousKey_Rotation(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	keyOld := bytes.Repeat([]byte("O"), 32)
	keyStranger := bytes.Repeat([]byte("S"), 32)

	codec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     string(keyNew),
		Previous:    string(keyOld),
		DevDefault:  "unused-dev-default",
		Label:       "audit",
	})
	require.NoError(t, err)

	// Token signed by the old key (operator deployed it before the rotation)
	// must still verify through the rotation-aware codec.
	codecOldOnly, err := query.NewCursorCodec(keyOld)
	require.NoError(t, err)
	oldToken, err := codecOldOnly.Encode(query.Cursor{Values: []any{"legacy"}})
	require.NoError(t, err)
	decoded, err := codec.Decode(oldToken)
	require.NoError(t, err, "previous key must enable rotation verification")
	assert.Equal(t, []any{"legacy"}, decoded.Values)

	// Stranger key tokens must still fail — rotation does not weaken auth.
	codecStranger, err := query.NewCursorCodec(keyStranger)
	require.NoError(t, err)
	strangerToken, err := codecStranger.Encode(query.Cursor{Values: []any{"x"}})
	require.NoError(t, err)
	_, err = codec.Decode(strangerToken)
	require.Error(t, err, "unknown-key tokens must still be rejected")
}

// TestBuildCursorCodec_PreviousKeyShortInRealMode_FailFast asserts that if the
// previous key is too short, codec construction fails fast at startup rather
// than silently ignoring the rotation intent.
func TestBuildCursorCodec_PreviousKeyShortInRealMode_FailFast(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	codec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     string(keyNew),
		Previous:    "too-short",
		DevDefault:  "unused",
		Label:       "audit",
	})
	require.Error(t, err)
	assert.Nil(t, codec)
	// Error must come from NewCursorCodec previous-key length check and be
	// wrapped so operators see the label + envName in the outer message.
	assert.Contains(t, err.Error(), "audit")
	// The wrap chain must remain traversable via errors.As all the way to the
	// concrete *errcode.Error with ErrCursorInvalid code — otherwise downstream
	// HTTP mappers and log aggregators that match on code will silently
	// fall back to generic 500. Harden the contract rather than best-effort it.
	// ref: Go errors pkg tests — Is/As/Unwrap invariants; Kratos errors_test.
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr,
		"buildCursorCodec must keep the errcode.Error reachable via errors.As")
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code,
		"short previous key must surface as ErrCursorInvalid regardless of wrap depth")
}

// TestBuildCursorCodec_PreviousKeyEmpty_OK confirms that if the previous key
// is empty, the codec still constructs successfully as single-key mode.
// Single-key is the default stable state; rotation is a temporary window.
func TestBuildCursorCodec_PreviousKeyEmpty_OK(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	codec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     string(keyNew),
		Previous:    "",
		DevDefault:  "unused",
		Label:       "audit",
	})
	require.NoError(t, err)
	require.NotNil(t, codec)

	// Round-trip sanity so we know the codec is live (not a nil trap).
	tok, err := codec.Encode(query.Cursor{Values: []any{"solo"}})
	require.NoError(t, err)
	decoded, err := codec.Decode(tok)
	require.NoError(t, err)
	assert.Equal(t, []any{"solo"}, decoded.Values)
}

// TestBuildCursorCodec_PreviousKeyDemoInRealMode_Rejected ensures the demo-key
// guard applies to the previous key too — an operator cannot leave a
// well-known demo value in the rotation slot.
func TestBuildCursorCodec_PreviousKeyDemoInRealMode_Rejected(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	_, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     string(keyNew),
		Previous:    "corebundle-audit-cursor-key-32b!",
		DevDefault:  "unused",
		Label:       "audit",
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "demo")
	assert.Contains(t, err.Error(), "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"error must name the previous-key env var")
}

// TestBuildCursorCodec_CurrentEqualsPrevious_Rejected asserts that when the
// current and previous cursor keys are equal, buildCursorCodec propagates the
// errcode.ErrCursorInvalid from NewCursorCodec.
// ref: S2-C1 — NewCursorCodec rejects identical current/previous keys.
func TestBuildCursorCodec_CurrentEqualsPrevious_Rejected(t *testing.T) {
	sameKey := bytes.Repeat([]byte("K"), 32)
	codec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     string(sameKey),
		Previous:    string(sameKey),
		DevDefault:  "unused",
		Label:       "audit",
	})
	require.Error(t, err)
	assert.Nil(t, codec)
	assert.Contains(t, err.Error(), "previous cursor key must differ from current",
		"error must explain why the keys were rejected")
}

// TestBuildCursorCodec_BothKeysShort_ReportsFirst asserts that when both
// current and previous keys are shorter than 32 bytes, the error reports the
// current key length first (NewCursorCodec validates current before previous).
func TestBuildCursorCodec_BothKeysShort_ReportsFirst(t *testing.T) {
	codec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: "",
		EnvName:     "GOCELL_TEST_CURSOR_KEY",
		PrevEnvName: "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		Primary:     "short-current-11",
		Previous:    "short-previous-1",
		DevDefault:  "unused-dev-default",
		Label:       "audit",
	})
	require.Error(t, err)
	assert.Nil(t, codec)
	// NewCursorCodec validates current length first; the error must mention
	// "cursor HMAC key" (current) not "previous cursor HMAC key".
	assert.Contains(t, err.Error(), "cursor HMAC key",
		"first error must be about the current key length")
}
