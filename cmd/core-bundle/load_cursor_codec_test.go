package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadCursorCodec_DevDefault_Succeeds confirms that in dev mode (empty
// adapterMode) the dev default secret produces a usable codec without env
// being set, matching loadSecret's dev fallback contract.
func TestLoadCursorCodec_DevDefault_Succeeds(t *testing.T) {
	t.Setenv("GOCELL_TEST_CURSOR_KEY", "")
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", "")

	codec, err := loadCursorCodec("", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"core-bundle-audit-cursor-key-32!", "audit")
	require.NoError(t, err)
	require.NotNil(t, codec)

	_, err = codec.Encode(query.Cursor{Values: []any{"x"}})
	require.NoError(t, err, "dev default codec must round-trip")
}

// TestLoadCursorCodec_RealModeMissingEnv_FailFast asserts that in adapter
// mode "real" an unset cursor env triggers loadSecret's hard-error branch and
// the error wrap chain preserves the env name for operator diagnosis.
func TestLoadCursorCodec_RealModeMissingEnv_FailFast(t *testing.T) {
	t.Setenv("GOCELL_TEST_CURSOR_KEY", "")
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", "")

	codec, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"core-bundle-audit-cursor-key-32!", "audit")
	require.Error(t, err)
	assert.Nil(t, codec)
	assert.Contains(t, err.Error(), "GOCELL_TEST_CURSOR_KEY",
		"error must name the missing env var")
	assert.Contains(t, err.Error(), "audit", "error must preserve label")
	assert.Contains(t, err.Error(), "adapter mode \"real\"",
		"error must indicate the mode that triggered fail-fast")
}

// TestLoadCursorCodec_DemoKeyInRealMode_Rejected asserts that even when the
// env is set, a well-known demo value is rejected in real mode. Reachable if
// an operator copies the dev default into prod config.
func TestLoadCursorCodec_DemoKeyInRealMode_Rejected(t *testing.T) {
	t.Setenv("GOCELL_TEST_CURSOR_KEY", "core-bundle-audit-cursor-key-32!")
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", "")

	_, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"core-bundle-audit-cursor-key-32!", "audit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "well-known demo key",
		"error must come from rejectDemoKey")
	assert.Contains(t, err.Error(), "GOCELL_TEST_CURSOR_KEY")
}

// TestLoadCursorCodec_WithPreviousKey_Rotation wires current+previous env
// vars and confirms the resulting codec verifies tokens signed by either key —
// the core invariant of the 3-step rotation lifecycle.
// ref: kube-apiserver --service-account-key-file (verification) +
//
//	--service-account-signing-key-file (signing) — two slots, decode tries all.
func TestLoadCursorCodec_WithPreviousKey_Rotation(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	keyOld := bytes.Repeat([]byte("O"), 32)
	keyStranger := bytes.Repeat([]byte("S"), 32)

	t.Setenv("GOCELL_TEST_CURSOR_KEY", string(keyNew))
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", string(keyOld))

	codec, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"unused-dev-default", "audit")
	require.NoError(t, err)

	// Token signed by the old key (operator deployed it before the rotation)
	// must still verify through the rotation-aware codec.
	codecOldOnly, err := query.NewCursorCodec(keyOld)
	require.NoError(t, err)
	oldToken, err := codecOldOnly.Encode(query.Cursor{Values: []any{"legacy"}})
	require.NoError(t, err)
	decoded, err := codec.Decode(oldToken)
	require.NoError(t, err, "previous key env must enable rotation verification")
	assert.Equal(t, []any{"legacy"}, decoded.Values)

	// Stranger key tokens must still fail — rotation does not weaken auth.
	codecStranger, err := query.NewCursorCodec(keyStranger)
	require.NoError(t, err)
	strangerToken, err := codecStranger.Encode(query.Cursor{Values: []any{"x"}})
	require.NoError(t, err)
	_, err = codec.Decode(strangerToken)
	require.Error(t, err, "unknown-key tokens must still be rejected")
}

// TestLoadCursorCodec_PreviousKeyShortInRealMode_FailFast asserts that if the
// previous key env is set to a too-short value the codec construction fails
// fast at startup rather than silently ignoring the rotation intent.
func TestLoadCursorCodec_PreviousKeyShortInRealMode_FailFast(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	t.Setenv("GOCELL_TEST_CURSOR_KEY", string(keyNew))
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", "too-short")

	codec, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"unused", "audit")
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
		"loadCursorCodec must keep the errcode.Error reachable via errors.As")
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code,
		"short previous key must surface as ErrCursorInvalid regardless of wrap depth")
}

// TestLoadCursorCodec_PreviousKeyUnset_OK confirms that if the previous-key
// env is not set, the codec still constructs successfully as single-key mode.
// Single-key is the default stable state; rotation is a temporary window.
func TestLoadCursorCodec_PreviousKeyUnset_OK(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	t.Setenv("GOCELL_TEST_CURSOR_KEY", string(keyNew))
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", "")

	codec, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"unused", "audit")
	require.NoError(t, err)
	require.NotNil(t, codec)

	// Round-trip sanity so we know the codec is live (not a nil trap).
	tok, err := codec.Encode(query.Cursor{Values: []any{"solo"}})
	require.NoError(t, err)
	decoded, err := codec.Decode(tok)
	require.NoError(t, err)
	assert.Equal(t, []any{"solo"}, decoded.Values)
}

// TestLoadCursorCodec_PreviousKeyDemoInRealMode_Rejected ensures the demo-key
// guard applies to the previous-key env too — an operator cannot leave a
// well-known demo value in the rotation slot.
func TestLoadCursorCodec_PreviousKeyDemoInRealMode_Rejected(t *testing.T) {
	keyNew := bytes.Repeat([]byte("N"), 32)
	t.Setenv("GOCELL_TEST_CURSOR_KEY", string(keyNew))
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", "core-bundle-audit-cursor-key-32!")

	_, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"unused", "audit")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "demo")
	assert.Contains(t, err.Error(), "GOCELL_TEST_CURSOR_PREVIOUS_KEY",
		"error must name the previous-key env var")
}

// TestLoadCursorCodec_CurrentEqualsPrevious_Rejected asserts that when the
// current and previous cursor keys are set to the same value, loadCursorCodec
// propagates the errcode.ErrCursorInvalid from NewCursorCodec.
// ref: S2-C1 — NewCursorCodec rejects identical current/previous keys.
func TestLoadCursorCodec_CurrentEqualsPrevious_Rejected(t *testing.T) {
	sameKey := bytes.Repeat([]byte("K"), 32)
	t.Setenv("GOCELL_TEST_CURSOR_KEY", string(sameKey))
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", string(sameKey))

	codec, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY", "unused", "audit")
	require.Error(t, err)
	assert.Nil(t, codec)
	assert.Contains(t, err.Error(), "previous cursor key must differ from current",
		"error must explain why the keys were rejected")
}

// TestLoadCursorCodec_OnlyPreviousSet_CurrentMissingRealMode_FailFast asserts
// that in real mode, if the current key env is unset but the previous key env
// is set, loadSecret fails fast on the missing current key before even loading
// the previous key — enforcing the correct error priority.
func TestLoadCursorCodec_OnlyPreviousSet_CurrentMissingRealMode_FailFast(t *testing.T) {
	t.Setenv("GOCELL_TEST_CURSOR_KEY", "")
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", string(bytes.Repeat([]byte("P"), 32)))

	codec, err := loadCursorCodec("real", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY", "unused", "audit")
	require.Error(t, err)
	assert.Nil(t, codec)
	// The current key error fires first; previous key is never reached.
	assert.Contains(t, err.Error(), "GOCELL_TEST_CURSOR_KEY",
		"error must report the missing current-key env var, not the previous")
}

// TestLoadCursorCodec_BothKeysShort_ReportsFirst asserts that when both current
// and previous keys are shorter than 32 bytes, the error reports the current
// key length first (loadSecret delegates to NewCursorCodec which validates
// current before previous).
func TestLoadCursorCodec_BothKeysShort_ReportsFirst(t *testing.T) {
	t.Setenv("GOCELL_TEST_CURSOR_KEY", "short-current-11")
	t.Setenv("GOCELL_TEST_CURSOR_PREVIOUS_KEY", "short-previous-1")

	codec, err := loadCursorCodec("", "GOCELL_TEST_CURSOR_KEY",
		"GOCELL_TEST_CURSOR_PREVIOUS_KEY", "unused-dev-default", "audit")
	require.Error(t, err)
	assert.Nil(t, codec)
	// NewCursorCodec validates current length first; the error must mention
	// "cursor HMAC key" (current) not "previous cursor HMAC key".
	assert.Contains(t, err.Error(), "cursor HMAC key",
		"first error must be about the current key length")
}
