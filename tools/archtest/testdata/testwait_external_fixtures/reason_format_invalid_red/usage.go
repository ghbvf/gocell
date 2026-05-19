//go:build archtest_fixture

// Package reason_format_invalid_red is a RED fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// reason is a literal but the value violates the kebab-case identifier format
// (uppercase letters, underscores, leading hyphen).
package reason_format_invalid_red

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

func UseExternal(t testing.TB) {
	testwait.External(t, "Bad_Reason", // violation on this line
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"non-kebab")
}
