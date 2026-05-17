package app

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/scaffoldid"
)

// mustID wraps scaffoldid.Parse with t.Fatal on validation error so cmd-layer
// test fixtures can stay terse. Only call with known-valid literal IDs;
// invalid strings cause t.Fatal. Replaces the previously-exported
// scaffoldid.MustParse to keep the package's "Parse is the sole public
// constructor" contract closed (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01).
func mustID(t testing.TB, raw string) scaffoldid.ScaffoldID {
	t.Helper()
	id, err := scaffoldid.Parse(raw)
	if err != nil {
		t.Fatalf("scaffoldid.Parse(%q): %v", raw, err)
	}
	return id
}
