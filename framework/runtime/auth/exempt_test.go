package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompilePasswordResetExempts_AcceptsValid exercises the happy path: a
// well-formed list compiles and the returned matcher honors method + template.
func TestCompilePasswordResetExempts_AcceptsValid(t *testing.T) {
	fn, err := CompilePasswordResetExempts([]string{
		"POST /api/v1/access/users/{id}/password",
		"DELETE /api/v1/access/sessions/{id}",
	})
	require.NoError(t, err)

	assert.True(t, fn("POST", "/api/v1/access/users/u1/password"))
	assert.True(t, fn("DELETE", "/api/v1/access/sessions/s1"))
	// Wrong method must not slip through the template match.
	assert.False(t, fn("GET", "/api/v1/access/sessions/s1"))
	// Unrelated path must not match.
	assert.False(t, fn("POST", "/api/v1/access/users/u1"))
}

// TestCompilePasswordResetExempts_EmptyReturnsFailClosedMatcher mirrors
// middleware.CompilePublicEndpoints: an empty list is a valid "exempt nothing"
// configuration and returns a matcher that always returns false.
func TestCompilePasswordResetExempts_EmptyReturnsFailClosedMatcher(t *testing.T) {
	fn, err := CompilePasswordResetExempts(nil)
	require.NoError(t, err)
	assert.False(t, fn("POST", "/anything"))
}

// TestCompilePasswordResetExempts_RejectsMalformedEntries covers the fail-fast
// parity with middleware.CompilePublicEndpoints — the reviewer finding that
// motivated unifying validation strength.
func TestCompilePasswordResetExempts_RejectsMalformedEntries(t *testing.T) {
	cases := []struct {
		name        string
		entry       string
		wantErrFrag string
	}{
		{name: "empty string", entry: "", wantErrFrag: "must not be empty"},
		{name: "no space separator", entry: "POST/foo", wantErrFrag: "METHOD /path"},
		{name: "invalid method token", entry: "GETT /foo", wantErrFrag: "not recognized"},
		{name: "lowercase invalid method", entry: "patc /foo", wantErrFrag: "not recognized"},
		{name: "relative path", entry: "POST foo/bar", wantErrFrag: "must start with '/'"},
		{name: "empty path after space", entry: "POST ", wantErrFrag: "METHOD /path"},
		{name: "only whitespace around path", entry: " /foo", wantErrFrag: "METHOD /path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompilePasswordResetExempts([]string{tc.entry})
			require.Error(t, err, "entry %q must be rejected", tc.entry)
			assert.Contains(t, err.Error(), tc.wantErrFrag,
				"error must explain why entry %q is invalid", tc.entry)
		})
	}
}

// TestCompilePasswordResetExempts_DetectsDuplicates verifies that repeated
// (method, path-template) pairs fail at startup rather than silently shrinking
// to a single matcher entry. This is the third leg of parity with
// middleware.CompilePublicEndpoints' fail-fast contract.
func TestCompilePasswordResetExempts_DetectsDuplicates(t *testing.T) {
	_, err := CompilePasswordResetExempts([]string{
		"POST /api/v1/access/users/{id}/password",
		"POST /api/v1/access/users/{id}/password",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

// TestCompilePasswordResetExempts_AggregatesErrors verifies that multiple
// malformed entries in the same list surface together via errors.Join so
// the operator sees every problem in a single startup failure instead of
// whack-a-mole. Mirrors CompilePublicEndpoints' errors.Join behavior.
func TestCompilePasswordResetExempts_AggregatesErrors(t *testing.T) {
	_, err := CompilePasswordResetExempts([]string{
		"GETT /foo",          // bad method
		"POST relative/path", // non-absolute
		"POST /x",            // valid
		"POST /x",            // duplicate of prior
	})
	require.Error(t, err)
	msg := err.Error()
	// Each bad entry must be indexed so the operator can locate it.
	assert.Contains(t, msg, "entry[0]")
	assert.Contains(t, msg, "entry[1]")
	assert.Contains(t, msg, "entry[3]")
	// Distinct error reasons must all appear.
	assert.Contains(t, msg, "not recognized")
	assert.Contains(t, msg, "must start with '/'")
	assert.Contains(t, msg, "duplicate")
	// Sanity: the aggregation should produce at least three failures — cheap
	// regression guard if someone replaces errors.Join with "return first error".
	assert.GreaterOrEqual(t, strings.Count(msg, "entry["), 3)
}
