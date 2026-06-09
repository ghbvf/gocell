package mem_test

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports/conformance"
)

// TestMemPolicyRepo_Conformance enrolls the in-memory PolicyRepository in the
// shared PolicyRepository conformance suite. Any implementation of
// ports.PolicyRepository must enroll here or in the equivalent _test.go file
// within its own package.
func TestMemPolicyRepo_Conformance(t *testing.T) {
	conformance.RunPolicyRepoConformance(t, memPolicyRepoFactory)
}

func memPolicyRepoFactory(t *testing.T) ports.PolicyRepository {
	t.Helper()
	return mem.NewPolicyRepository()
}
