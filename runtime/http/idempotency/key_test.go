package idempotency

import "testing"

// TestDeriveCommandKey covers the #1669 command dedup key derivation: the
// (ns,key) layout, the empty-tenant sentinel, the NUL separator, and the
// per-dimension isolation (distinct tenant/subject/commandID → distinct slot).
// DeriveCommandKey is node-agnostic by construction — it reads only its three
// params, no pod/listener/cell input (the β archtest signature freeze enforces
// the absence of a 4th param).

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
