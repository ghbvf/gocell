package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestInfraInstanceKey_Equality verifies the sealed key behaves as a value
// identity: same id compares equal, different id compares unequal, and the
// type is usable as a Go map key (comparable). This is the foundation of the
// per-instance relay fan-out collection (#2152 PR-1).
func TestInfraInstanceKey_Equality(t *testing.T) {
	t.Parallel()

	a1 := NewInfraInstanceKey("accesscore")
	a2 := NewInfraInstanceKey("accesscore")
	b := NewInfraInstanceKey("auditcore")

	if a1 != a2 {
		t.Fatalf("NewInfraInstanceKey(%q) must equal an identical-id key", "accesscore")
	}
	if a1 == b {
		t.Fatalf("distinct ids must produce distinct keys: %v == %v", a1, b)
	}

	// Usable as a map key (the relay fan-out collection keys on this type).
	m := map[InfraInstanceKey]int{a1: 1, b: 2}
	if m[a2] != 1 {
		t.Fatalf("key with identical id must hit the same map slot; got %d", m[a2])
	}
	if len(m) != 2 {
		t.Fatalf("two distinct keys must occupy two slots; got %d", len(m))
	}
}

// TestDefaultInstanceKey_IsZeroValueAndDistinct verifies the colocated sentinel:
// DefaultInstanceKey() is the zero value (so a struct-zero key resolves to the
// single colocated instance), is stable across calls, and never collides with a
// minted non-default key (which always carries a non-empty id).
func TestDefaultInstanceKey_IsZeroValueAndDistinct(t *testing.T) {
	t.Parallel()

	var zero InfraInstanceKey
	d1 := DefaultInstanceKey()
	d2 := DefaultInstanceKey()
	if d1 != zero {
		t.Fatal("DefaultInstanceKey() must be the zero value (colocated sentinel)")
	}
	if d1 != d2 {
		t.Fatal("DefaultInstanceKey() must be stable across calls")
	}
	if DefaultInstanceKey() == NewInfraInstanceKey("accesscore") {
		t.Fatal("a real instance key must not collide with the default sentinel")
	}
}

// TestNewInfraInstanceKey_RejectsMalformedID verifies the mint-time contract:
// the id must be a non-empty lowercase snake_case identifier (<=32 chars) so it
// composes into a valid healthz probe name. Malformed ids panic at the source.
func TestNewInfraInstanceKey_RejectsMalformedID(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":               "",
		"hyphen":              "pool-a",
		"uppercase":           "PoolA",
		"leading digit":       "1pool",
		"trailing underscore": "pool_",
		"double underscore":   "pool__a",
		"whitespace":          "pool a",
		"too long":            "this_identifier_is_far_too_long_to_be_valid",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Panics(t, func() { _ = NewInfraInstanceKey(id) },
				"NewInfraInstanceKey(%q) must panic on a malformed instance id", id)
		})
	}

	// Valid ids must NOT panic.
	for _, id := range []string{"accesscore", "pool_a", "pool0", "a"} {
		assert.NotPanics(t, func() { _ = NewInfraInstanceKey(id) },
			"NewInfraInstanceKey(%q) must accept a valid snake_case id", id)
	}
}
