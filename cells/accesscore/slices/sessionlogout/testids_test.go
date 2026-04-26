package sessionlogout

import "github.com/google/uuid"

// testID returns a deterministic canonical lowercase UUID for the given label.
// Used so handler-level tests pass the UUID-validator added in PR-A45 while
// keeping the source readable (label = test fixture identity).
var testNamespace = uuid.NewSHA1(uuid.Nil, []byte("gocell-pr-a45-test"))

func testID(label string) string {
	return uuid.NewSHA1(testNamespace, []byte(label)).String()
}
