//go:build integration

package postgres

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports/conformance"
)

// TestPGPolicyRepo_Conformance runs the shared ports.PolicyRepository conformance
// suite against the PG-backed store. This call is what enrolls PGPolicyRepo under
// POLICYREPO-CONFORMANCE-ENROLLMENT-01 (#1346 PR-8): the archtest collects every
// concrete ports.PolicyRepository implementation and fails CI unless this
// package contains a conformance.RunPolicyRepoConformance call.
//
// Each sub-test receives a fresh per-test database (setupPolicyRepoPG →
// sharedPG.NewPerTestPool), so the factory's "zero-state repository" contract is
// satisfied. RLS is FORCE-enabled on the policies table (migration 058); the
// superuser test pool bypasses RLS, so the suite's CrossTenant_Isolation passes
// via the application-level WHERE tenant_id predicates (identical to the PG
// user/role conformance under their own FORCE RLS tables).
func TestPGPolicyRepo_Conformance(t *testing.T) {
	conformance.RunPolicyRepoConformance(t, pgPolicyRepoFactory)
}

func pgPolicyRepoFactory(t *testing.T) ports.PolicyRepository {
	repo, _ := setupPolicyRepoPG(t)
	return repo
}
