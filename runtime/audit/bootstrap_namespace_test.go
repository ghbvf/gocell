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
		"bootstrap namespace literal must satisfy NamespaceID.Validate (lowercase / [a-z_] first char / length ≤ 48 / no ':' '{' '}')")
}

// TestBootstrapNamespace_DistinctFromAuditcore is the partition invariant:
// the bootstrap chain MUST live on a different namespace than the auditcore
// relay chain, otherwise both writers can fork a shared HMAC chain (#1121).
func TestBootstrapNamespace_DistinctFromAuditcore(t *testing.T) {
	t.Parallel()
	assert.NotEqual(t, "auditcore", string(audit.BootstrapNamespace()),
		"bootstrap chain namespace must differ from auditcore relay chain namespace")
}
