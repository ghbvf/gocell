package redaction_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/redaction"
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
