package mem

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports/conformance"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

func TestMemUserRepo_Conformance(t *testing.T) {
	conformance.RunUserRepoConformance(t, memUserRepoFactory, conformance.Features{
		RequiresAmbientTx:         false,
		SupportsForUpdateLockHold: false,
		SupportsCASConflict:       false,
	})
}

func memUserRepoFactory(t *testing.T) (ports.UserRepository, persistence.TxRunner, func()) {
	t.Helper()
	s := NewStore(clock.Real())
	return s.UserRepository(), s.TxRunner(), func() {}
}
