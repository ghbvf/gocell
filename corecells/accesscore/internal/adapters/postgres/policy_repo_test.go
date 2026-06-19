package postgres

// policy_repo_test.go — white-box (package-internal) unit coverage for scanPolicy's
// stored-read validation profile (#1979 / PR #2409 F1). scanPolicy is the single
// PG read-side re-validation entry (GetByID / ListByTenant / the Delete-return
// reconstruction all funnel through it). It must apply a STORED-READ profile
// (structural integrity only) rather than the AUTHORING profile — otherwise a
// legacy persisted empty-Action allow row (valid before #1979; untargeted =
// match-all) is rejected as ErrPGSchemaShape on read and a single such row turns
// the entire tenant's PDP hot path into a 503, instead of that one rule being
// inert in the evaluator (the committed read-side defense-in-depth posture).
//
// These tests need no database: they drive scanPolicy via a fake policyRowScanner
// fed bytes produced by the same marshalRules used by the write path.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// validScanTenant is a canonical lowercase UUID accepted by tenant.TenantID.Validate.
const validScanTenant = tenant.TenantID("11111111-1111-1111-1111-111111111111")

// fakeRowScanner satisfies policyRowScanner, populating scanPolicy's five Scan
// destinations (id, name, description, rulesJSON, version) in order.
type fakeRowScanner struct {
	id          string
	name        string
	description string
	rulesJSON   []byte
	version     int
}

func (f *fakeRowScanner) Scan(dest ...any) error {
	*dest[0].(*string) = f.id
	*dest[1].(*string) = f.name
	*dest[2].(*string) = f.description
	*dest[3].(*[]byte) = f.rulesJSON
	*dest[4].(*int) = f.version
	return nil
}

// scanRow marshals rules to the durable JSONB shape (via the same marshalRules the
// write path uses, which does NOT validate) and reconstructs a row through scanPolicy.
func scanRow(t *testing.T, rules []abac.Rule) (*abac.Policy, error) {
	t.Helper()
	rulesJSON, err := marshalRules(rules)
	require.NoError(t, err)
	return scanPolicy(&fakeRowScanner{
		id:        "p1",
		name:      "stored policy",
		rulesJSON: rulesJSON,
		version:   1,
	}, validScanTenant)
}

// TestScanPolicy_LegacyEmptyActionAllow_StoredReadTolerant is the F1 regression
// (#2409): a persisted empty-Action allow row — written legitimately before #1979
// added the authoring non-empty-Action rule — must survive scanPolicy and reach
// the evaluator (which treats it as inert / never-granting), NOT be rejected as a
// storage-shape error. Empty-Action rejection is an AUTHORING invariant (write →
// 422), not a storage-integrity one, so the stored-read profile does not apply it.
func TestScanPolicy_LegacyEmptyActionAllow_StoredReadTolerant(t *testing.T) {
	// Empty Action + EffectAllow: the exact legacy untargeted-allow shape.
	got, err := scanRow(t, []abac.Rule{
		{ID: "r1", Name: "legacy untargeted allow", Effect: authz.EffectAllow},
	})

	require.NoError(t, err,
		"stored-read must tolerate a legacy empty-Action allow row (the evaluator "+
			"renders it inert), not reject it as ErrPGSchemaShape and 503 the whole tenant PDP")
	require.NotNil(t, got)
	require.Len(t, got.Rules, 1)
	assert.Empty(t, got.Rules[0].Action, "the empty Action must round-trip faithfully")
	assert.Equal(t, authz.EffectAllow, got.Rules[0].Effect)
}

// TestScanPolicy_CorruptRow_StillRejected is the anti-vacuity control: the
// stored-read profile must still reject GENUINE structural-integrity violations.
// An empty rule ID is decode-valid JSON but a corrupt aggregate; scanPolicy must
// route it to ErrPGSchemaShape (→ evaluator fail-closed), proving the tolerance
// above is scoped to empty-Action allow and not a blanket "accept anything".
func TestScanPolicy_CorruptRow_StillRejected(t *testing.T) {
	_, err := scanRow(t, []abac.Rule{
		// Non-empty Action so this is NOT the tolerated case; empty ID is the corruption.
		{ID: "", Name: "no id", Effect: authz.EffectAllow, Action: []string{"user:read"}},
	})

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "a corrupt persisted row must surface as a typed errcode error")
	assert.Equal(t, errcode.ErrPGSchemaShape, ec.Code,
		"a genuine structural-integrity violation must still fail-closed as ErrPGSchemaShape")
}
