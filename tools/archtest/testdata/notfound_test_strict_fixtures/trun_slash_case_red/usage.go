// Package trun_slash_case_red proves that t.Run subtest names containing
// '/' (Go testing's path separator for -run filters), spaces, or other
// non-[A-Za-z0-9_] characters but still ending in _NotFound are correctly
// matched by tRunCaseNameMatches and required to contain a funnel call.
// Without the strings.HasSuffix predicate this case would silently miss.
// t.Run at line 15 — the body intentionally lacks any funnel call.
package trun_slash_case_red

import (
	"errors"
	"testing"
)

func TestRepo_Paths(t *testing.T) {
	t.Run("Get/missing_NotFound", func(t *testing.T) {
		_ = errors.New("simulated") // no funnel call
	})
}
