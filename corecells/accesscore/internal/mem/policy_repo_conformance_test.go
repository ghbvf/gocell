package mem_test

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports/conformance"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
)

// TestMemPolicyRepo_Conformance enrolls the in-memory PolicyRepository in the
// shared PolicyRepository conformance suite. Any implementation of
// ports.PolicyRepository must enroll here or in the equivalent _test.go file
// within its own package.
func TestMemPolicyRepo_Conformance(t *testing.T) {
	conformance.RunPolicyRepoConformance(t, memPolicyRepoFactory)
}

// TestMemPolicyRepo_RepoReadiness enrolls the in-memory PolicyRepository in the
// healthz.RepoProber readiness conformance (CELL-REPO-READYZ-PROBE-01). The mem
// store is always ready and has no differentiated failure domain, so the broken
// prober is nil (the schema-broken sub-test is skipped).
func TestMemPolicyRepo_RepoReadiness(t *testing.T) {
	celltest.RunRepoReadinessConformance(t, "accesscore-policy-mem", mem.NewPolicyRepository(), nil)
}

func memPolicyRepoFactory(t *testing.T) ports.PolicyRepository {
	t.Helper()
	return mem.NewPolicyRepository()
}
