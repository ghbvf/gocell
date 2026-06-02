package main

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestStarterPrefixRegistered verifies that the package init() in prefix.go
// registers "ERR_STARTER_" under the corebundlestarter owner.
// The init() runs before any test, so the registry entry is already present.
func TestStarterPrefixRegistered(t *testing.T) {
	owner, ok := errcode.OwnerOfCode("ERR_STARTER_SOMETHING")
	if !ok {
		t.Fatal("OwnerOfCode(\"ERR_STARTER_SOMETHING\") returned ok=false; want registered owner")
	}
	const want = "github.com/ghbvf/gocell/examples/corebundlestarter"
	if owner != want {
		t.Errorf("OwnerOfCode(\"ERR_STARTER_SOMETHING\") = %q; want %q", owner, want)
	}
}
