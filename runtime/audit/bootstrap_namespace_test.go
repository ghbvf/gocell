package audit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/audit"
)

// TestBootstrapNamespace_LiteralPassesValidate confirms the bootstrap
// namespace literal complies with NamespaceID format rules — the const is
// validated at unit-test time so any future drift in the validator surfaces
// before composition root touches the literal.
func TestBootstrapNamespace_LiteralPassesValidate(t *testing.T) {
	t.Parallel()
	ns := audit.BootstrapNamespace()
	require.NoError(t, ns.Validate(),
		"bootstrap namespace literal must satisfy NamespaceID.Validate (non-empty / [a-z_] only / length ≤ 48)")
}

// TestBootstrapNamespace_NotEmpty is the partition invariant: the bootstrap
// chain namespace must be non-empty (otherwise it would coincide with the
// default zero value used by misconfigured stores). Distinctness from the
// auditcore relay chain is enforced by the AUDIT-NS-DISJOINT-01 archtest,
// which scans cmd/corebundle production wiring and resolves both namespace
// constants through the same code path — that is the authoritative
// "distinctness" check; a unit test that asserts the literal `"auditcore"`
// would pass vacuously if the relay namespace is ever renamed.
func TestBootstrapNamespace_NotEmpty(t *testing.T) {
	t.Parallel()
	assert.NotEmpty(t, string(audit.BootstrapNamespace()),
		"bootstrap chain namespace must not be the empty string")
}
