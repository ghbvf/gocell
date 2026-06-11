package mem

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports/conformance"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

func TestMemRoleRepo_Conformance(t *testing.T) {
	conformance.RunRoleRepoConformance(t, memRoleRepoFactory)
}

func memRoleRepoFactory(t *testing.T) (ports.RoleRepository, ports.UserRepository, persistence.TxRunner, func()) {
	t.Helper()
	s := NewStore(clock.Real())
	return s.RoleRepository(), s.UserRepository(), s.TxRunner(), func() {}
}
