package redaction_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

func TestHashIP_Deterministic(t *testing.T) {
	t.Parallel()
	salt := []byte("test-ip-hash-salt-32-bytes-pad!!")
	a := redaction.HashIP(salt, "203.0.113.7")
	b := redaction.HashIP(salt, "203.0.113.7")
	if a.String() != b.String() {
		t.Fatalf("same salt+ip must hash identically: %q != %q", a.String(), b.String())
	}
	if a.IsEmpty() {
		t.Fatal("non-empty ip must produce non-empty IPHash")
	}
	// hex of SHA-256 is 64 chars.
	if got := len(a.String()); got != 64 {
		t.Fatalf("HMAC-SHA256 hex length = %d, want 64", got)
	}
}

func TestHashIP_DifferentSaltDiffersHash(t *testing.T) {
	t.Parallel()
	ip := "203.0.113.7"
	a := redaction.HashIP([]byte("salt-A-32-bytes-padding-aaaaaa!!"), ip)
	b := redaction.HashIP([]byte("salt-B-32-bytes-padding-bbbbbb!!"), ip)
	if a.String() == b.String() {
		t.Fatal("different salts must yield different hashes (keyed HMAC)")
	}
}

func TestHashIP_DifferentIPDiffersHash(t *testing.T) {
	t.Parallel()
	salt := []byte("test-ip-hash-salt-32-bytes-pad!!")
	a := redaction.HashIP(salt, "203.0.113.7")
	b := redaction.HashIP(salt, "203.0.113.8")
	if a.String() == b.String() {
		t.Fatal("different IPs must yield different hashes")
	}
}

func TestHashIP_NotReversibleToPlaintext(t *testing.T) {
	t.Parallel()
	salt := []byte("test-ip-hash-salt-32-bytes-pad!!")
	ip := "203.0.113.7"
	h := redaction.HashIP(salt, ip)
	if strings.Contains(h.String(), ip) {
		t.Fatalf("hash %q must not contain plaintext ip %q", h.String(), ip)
	}
}

func TestHashIP_IPv6_Deterministic(t *testing.T) {
	t.Parallel()
	salt := []byte("test-ip-hash-salt-32-bytes-pad!!")
	// HashIP hashes the input string verbatim (no IP normalization); callers
	// pass whatever ctxkeys.RealIP holds.
	a := redaction.HashIP(salt, "2001:db8::1")
	b := redaction.HashIP(salt, "2001:db8::1")
	if a.String() != b.String() {
		t.Fatal("same salt+IPv6 must hash identically")
	}
	if a.IsEmpty() || len(a.String()) != 64 {
		t.Fatalf("IPv6 hash must be a 64-hex digest, got %q", a.String())
	}
	if v4 := redaction.HashIP(salt, "203.0.113.7"); a.String() == v4.String() {
		t.Fatal("distinct IPv6 vs IPv4 strings must hash differently")
	}
}

// TestHashIP_ShortSalt_StillHashes documents that HashIP itself does NOT validate
// the salt (it cannot return an error): a short/empty salt produces a valid-looking
// digest with no secrecy. The ≥MinIPHashSaltBytes guarantee is enforced at the
// composition root (cellmodules/accesscore), not here.
func TestHashIP_ShortSalt_StillHashes(t *testing.T) {
	t.Parallel()
	if redaction.MinIPHashSaltBytes != 32 {
		t.Fatalf("MinIPHashSaltBytes = %d, want 32 (matches auth HMAC key min)", redaction.MinIPHashSaltBytes)
	}
	h := redaction.HashIP([]byte("short"), "203.0.113.7")
	if h.IsEmpty() || len(h.String()) != 64 {
		t.Fatalf("HashIP must still produce a digest with a short salt (insecurity enforced at root), got %q", h.String())
	}
}

func TestHashIP_EmptyIP_ZeroValue(t *testing.T) {
	t.Parallel()
	h := redaction.HashIP([]byte("test-ip-hash-salt-32-bytes-pad!!"), "")
	if !h.IsEmpty() {
		t.Fatal("empty ip must produce the zero IPHash (IsEmpty)")
	}
	if h.String() != "" {
		t.Fatalf("empty IPHash String() = %q, want \"\"", h.String())
	}
	var zero redaction.IPHash
	if h != zero {
		t.Fatal("empty ip must equal the IPHash zero value")
	}
}

func TestIsIPHashString(t *testing.T) {
	t.Parallel()
	salt := []byte("test-ip-hash-salt-32-bytes-pad!!")
	// A real HashIP output must validate (round-trip with the wire shape).
	if got := redaction.HashIP(salt, "203.0.113.7"); !redaction.IsIPHashString(got.String()) {
		t.Fatalf("HashIP output %q must satisfy IsIPHashString", got.String())
	}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"64 lowercase hex", strings.Repeat("a1b2c3d4", 8), true},
		{"64 zeros", strings.Repeat("0", 64), true},
		{"plaintext ip", "192.0.2.1", false},
		{"too short", "a1b2c3d4", false},
		{"63 chars", strings.Repeat("a", 63), false},
		{"65 chars", strings.Repeat("a", 65), false},
		{"uppercase hex", strings.Repeat("A1B2C3D4", 8), false},
		{"non-hex 64", strings.Repeat("z", 64), false},
	}
	for _, tc := range cases {
		if got := redaction.IsIPHashString(tc.in); got != tc.want {
			t.Errorf("IsIPHashString(%q) = %v, want %v (%s)", tc.in, got, tc.want, tc.name)
		}
	}
}

func TestHashIP_MarshalJSON(t *testing.T) {
	t.Parallel()
	salt := []byte("test-ip-hash-salt-32-bytes-pad!!")

	h := redaction.HashIP(salt, "203.0.113.7")
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal IPHash: %v", err)
	}
	if want := `"` + h.String() + `"`; string(b) != want {
		t.Fatalf("MarshalJSON = %s, want %s", b, want)
	}

	// Zero value marshals to an empty JSON string (wire-compatible with the
	// optional schema field).
	zb, err := json.Marshal(redaction.IPHash{})
	if err != nil {
		t.Fatalf("marshal zero IPHash: %v", err)
	}
	if string(zb) != `""` {
		t.Fatalf("zero IPHash MarshalJSON = %s, want \"\"", zb)
	}

	// As an embedded field it serializes as a plain JSON string, matching the
	// payload schema's {"type": "string"} clientIpHash field.
	type wire struct {
		ClientIPHash redaction.IPHash `json:"clientIpHash"`
	}
	wb, err := json.Marshal(wire{ClientIPHash: h})
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	if want := `{"clientIpHash":"` + h.String() + `"}`; string(wb) != want {
		t.Fatalf("wire marshal = %s, want %s", wb, want)
	}
}
