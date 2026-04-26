// Package testutil provides shared test helpers for the accesscore cell.
package testutil

import "github.com/google/uuid"

var testNamespace = uuid.NewSHA1(uuid.Nil, []byte("gocell-pr-a45-test"))

// TestID returns a deterministic canonical lowercase UUID for the given label.
// Used so handler-level tests pass the UUID-validator added in PR-A45 while
// keeping the source readable (label = test fixture identity).
func TestID(label string) string {
	return uuid.NewSHA1(testNamespace, []byte(label)).String()
}
