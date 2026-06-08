package idempotency

import "testing"

// These tests cover the #1669 command dedup key derivation: the (ns,key) layout,
// the empty-tenant sentinel, the NUL separator, the empty-subject/empty-commandID
// degenerate boundaries, and the per-dimension isolation (distinct
// tenant/subject/commandID → distinct slot). DeriveCommandKey is node-agnostic by
// construction — it reads only its three params, no pod/listener/cell input (the β
// archtest signature freeze enforces the absence of a 4th param).

func TestDeriveCommandKey_Layout(t *testing.T) {
	t.Parallel()
	k := DeriveCommandKey("tenant-1", "sub-9", "cmd-42")
	if got, want := k.Namespace(), "tenant-1"; got != want {
		t.Errorf("Namespace() = %q, want %q", got, want)
	}
	// key = subject + NUL + commandID — exactly one separator, no method/path.
	if got, want := k.Key(), "sub-9\x00cmd-42"; got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestDeriveCommandKey_EmptyTenantSentinel(t *testing.T) {
	t.Parallel()
	k := DeriveCommandKey("", "sub", "cmd")
	if got, want := k.Namespace(), noTenantSentinel; got != want {
		t.Errorf("empty tenant Namespace() = %q, want sentinel %q", got, want)
	}
	// A service principal (no tenant) still gets a deterministic key body.
	if got, want := k.Key(), "sub\x00cmd"; got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

// TestDeriveCommandKey_DegenerateBoundaries locks the empty-subject and
// empty-commandID bodies — both are deterministic (the NUL separator always
// present), so a service principal (no subject) is distinguished within its
// tenant namespace by commandID alone, and an empty commandID by subject alone.
// The caller is responsible for keeping the surviving dimension unique.
func TestDeriveCommandKey_DegenerateBoundaries(t *testing.T) {
	t.Parallel()
	// Empty subject (service principal): slot keyed by commandID within ns.
	if got, want := DeriveCommandKey("t", "", "cmd").Key(), "\x00cmd"; got != want {
		t.Errorf("empty subject Key() = %q, want %q", got, want)
	}
	// Empty commandID: body is still well-formed (one NUL), subject distinguishes.
	if got, want := DeriveCommandKey("t", "sub", "").Key(), "sub\x00"; got != want {
		t.Errorf("empty commandID Key() = %q, want %q", got, want)
	}
	// Empty subject AND commandID is the lone all-empty body — bare separator.
	if got, want := DeriveCommandKey("t", "", "").Key(), "\x00"; got != want {
		t.Errorf("both-empty Key() = %q, want %q", got, want)
	}
}

func TestDeriveCommandKey_DistinctDimensions(t *testing.T) {
	t.Parallel()
	base := DeriveCommandKey("t", "sub", "cmd")

	// Distinct commandID → distinct key, same namespace (same tenant).
	if other := DeriveCommandKey("t", "sub", "cmd2"); other.Key() == base.Key() {
		t.Error("distinct commandID must yield distinct Key()")
	} else if other.Namespace() != base.Namespace() {
		t.Error("same tenant must yield same Namespace() regardless of commandID")
	}

	// Distinct subject → distinct key.
	if other := DeriveCommandKey("t", "sub2", "cmd"); other.Key() == base.Key() {
		t.Error("distinct subject must yield distinct Key()")
	}

	// Distinct tenant → distinct namespace, same key body.
	if other := DeriveCommandKey("t2", "sub", "cmd"); other.Namespace() == base.Namespace() {
		t.Error("distinct tenant must yield distinct Namespace()")
	} else if other.Key() != base.Key() {
		t.Error("same subject+commandID must yield same Key() across tenants")
	}
}

// TestDeriveCommandKey_NULSeparatorNoCollision proves the NUL separator removes
// the subject/commandID boundary ambiguity (the godoc's alic|e:x rationale): a
// plain separator-free concatenation would collide here.
func TestDeriveCommandKey_NULSeparatorNoCollision(t *testing.T) {
	t.Parallel()
	a := DeriveCommandKey("t", "ab", "c")
	b := DeriveCommandKey("t", "a", "bc")
	if a.Key() == b.Key() {
		t.Errorf("subject/commandID boundary collision: %q == %q", a.Key(), b.Key())
	}
}

// TestDeriveCommandKey_NoMethodPathDimension pins the "no method/path" decision
// structurally: a command key body has exactly one NUL (two segments), unlike a
// DeriveKey body's four NULs (five segments).
func TestDeriveCommandKey_NoMethodPathDimension(t *testing.T) {
	t.Parallel()
	cmd := DeriveCommandKey("t", "sub", "cmd")
	if n := countNUL(cmd.Key()); n != 1 {
		t.Errorf("command Key() has %d NUL separators, want 1 (subject\\x00commandID)", n)
	}
	http := DeriveKey("t", "sub", "POST", "/x", "idem")
	if n := countNUL(http.Key()); n != 3 {
		t.Errorf("HTTP Key() has %d NUL separators, want 3 (sanity anchor)", n)
	}
}

func countNUL(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			n++
		}
	}
	return n
}

// TestFlat_Layout pins the Flat() encoding: ns + NUL + key, node-agnostic. This
// is the sole flattening of a sealed IdempotencyKey into the string key that
// kernel/idempotency.Claimer.Claim consumes.
func TestFlat_Layout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		tenant, sub, cmd string
		want             string
	}{
		{
			name:   "tenant + subject + commandID",
			tenant: "tenant-1", sub: "sub-9", cmd: "cmd-42",
			want: "tenant-1\x00sub-9\x00cmd-42",
		},
		{
			name:   "empty tenant uses _notenant sentinel",
			tenant: "", sub: "sub-9", cmd: "cmd-42",
			want: "_notenant\x00sub-9\x00cmd-42",
		},
		{
			name:   "empty subject (service principal) distinguished by commandID",
			tenant: "tenant-1", sub: "", cmd: "cmd-42",
			want: "tenant-1\x00\x00cmd-42",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			k := DeriveCommandKey(tt.tenant, tt.sub, tt.cmd)
			if got := k.Flat(); got != tt.want {
				t.Errorf("Flat() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFlat_DistinctKeysFlattenDistinct asserts the flattening preserves the
// per-dimension isolation of the sealed (ns,key) pair: keys that differ in any
// dimension produce different flat strings (no collision via flattening).
func TestFlat_DistinctKeysFlattenDistinct(t *testing.T) {
	t.Parallel()
	base := DeriveCommandKey("t", "s", "c").Flat()
	for _, other := range []string{
		DeriveCommandKey("t2", "s", "c").Flat(),
		DeriveCommandKey("t", "s2", "c").Flat(),
		DeriveCommandKey("t", "s", "c2").Flat(),
	} {
		if other == base {
			t.Errorf("distinct command key flattened to same string as base: %q", base)
		}
	}
}
