package sessiontest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth/session/sessiontest"
)

// TestProtocol_HappyPath verifies sessiontest.Protocol() returns a non-nil
// *session.Protocol configured with the canonical fingerprint / ordering /
// revoke-on-all options used by every cross-package test helper.
func TestProtocol_HappyPath(t *testing.T) {
	t.Parallel()
	p := sessiontest.Protocol()
	require.NotNil(t, p)
	// session.Protocol exposes accessors for fingerprint / ordering — the
	// canonical helper installs FingerprintJTIRef + OrderingAuthzEpoch, but
	// shape assertions live in session_test; here we only verify the helper
	// returned a non-nil instance, which exercises the entire happy-path body
	// of Protocol().
	assert.NotNil(t, p.Fingerprint())
	assert.NotNil(t, p.Ordering())
}
