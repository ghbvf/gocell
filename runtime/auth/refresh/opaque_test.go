package refresh

import (
	"bytes"
	"crypto/rand"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeOpaque_DeterministicLength(t *testing.T) {
	sel := bytes.Repeat([]byte{0xAB}, SelectorLen)
	ver := bytes.Repeat([]byte{0xCD}, VerifierLen)
	wire := EncodeOpaque(sel, ver)
	assert.Len(t, wire, WireLen, "wire token length must be 66 chars (22 + 1 + 43)")
	assert.Equal(t, 1, strings.Count(wire, "."), "wire token must have exactly one separator dot")
	assert.NotContains(t, wire, "=", "base64url no-padding must emit no padding char")
}

func TestEncodeParse_RoundTrip(t *testing.T) {
	for i := 0; i < 32; i++ {
		sel := make([]byte, SelectorLen)
		ver := make([]byte, VerifierLen)
		_, err := io.ReadFull(rand.Reader, sel)
		require.NoError(t, err)
		_, err = io.ReadFull(rand.Reader, ver)
		require.NoError(t, err)

		wire := EncodeOpaque(sel, ver)
		gotSel, gotVer, ok := ParseOpaque(wire)
		require.True(t, ok, "parse must succeed for wire produced by EncodeOpaque")
		assert.Equal(t, sel, gotSel)
		assert.Equal(t, ver, gotVer)
	}
}

func TestParseOpaque_RejectsMalformedInputs(t *testing.T) {
	validWire := EncodeOpaque(bytes.Repeat([]byte{0x01}, SelectorLen), bytes.Repeat([]byte{0x02}, VerifierLen))
	selPart, verPart, _ := strings.Cut(validWire, ".")

	cases := []struct {
		name string
		in   string
	}{
		{"empty string", ""},
		{"wrong length (too short)", "short"},
		{"wrong length (just under 66)", validWire[:WireLen-1]},
		{"wrong length (just over 66)", validWire + "x"},
		{"no separator", strings.Repeat("a", WireLen)},
		{"two separators", selPart + "." + verPart + "." + ""},
		{"empty selector half", "." + verPart},
		{"empty verifier half", selPart + "."},
		{"non-base64 selector", strings.Repeat("!", 22) + "." + verPart},
		{"non-base64 verifier", selPart + "." + strings.Repeat("!", 43)},
		{"selector too short after decode", EncodeOpaque(bytes.Repeat([]byte{0x01}, SelectorLen-1), bytes.Repeat([]byte{0x02}, VerifierLen))},
		{"verifier too short after decode", EncodeOpaque(bytes.Repeat([]byte{0x01}, SelectorLen), bytes.Repeat([]byte{0x02}, VerifierLen-1))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := ParseOpaque(tc.in)
			assert.False(t, ok, "malformed input must return ok=false (got parse success)")
		})
	}
}

func TestParseOpaque_AccessJWTRejected(t *testing.T) {
	// A canonical JWT has 3 base64url segments joined by dots. It will fail
	// the length check (typical JWT > 66 chars) or the dot-count check (2 dots).
	// This preserves the pre-X15 guarantee that an access JWT presented at the
	// refresh endpoint is fail-closed.
	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1MSJ9.SignedPart"
	_, _, ok := ParseOpaque(jwt)
	assert.False(t, ok, "access JWT must fail ParseOpaque (distinct from opaque wire format)")
}
