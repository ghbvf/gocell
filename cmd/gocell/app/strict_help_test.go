package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStrictFlagUsage_ListsAllStrictRules locks PR #1018 review finding F1: the
// --strict help is derived from governance.StrictRuleCodes, so it must list
// every PhaseStrict rule — including FMT-19 and DOC-NAME-01, which the old
// hand-maintained help string silently omitted.
func TestStrictFlagUsage_ListsAllStrictRules(t *testing.T) {
	t.Parallel()
	usage := strictFlagUsage()
	for _, code := range []string{"VERIFY-06", "FMT-16", "FMT-17", "FMT-19", "DOC-NAME-01"} {
		assert.Contains(t, usage, code, "--strict help must list %s", code)
	}
}
