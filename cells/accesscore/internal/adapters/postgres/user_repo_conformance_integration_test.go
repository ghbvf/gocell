//go:build integration

package postgres

import (
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports/conformance"
	"github.com/ghbvf/gocell/kernel/persistence"
)

func TestPGUserRepo_Conformance(t *testing.T) {
	conformance.RunUserRepoConformance(t, pgUserRepoFactory(t), conformance.Features{
		RequiresAmbientTx:         true,
		SupportsForUpdateLockHold: true,
		SupportsCASConflict:       true,
	})
}

func pgUserRepoFactory(t *testing.T) conformance.UserRepoFactory {
	t.Helper()
	return func(t *testing.T) (ports.UserRepository, persistence.TxRunner, func()) {
		t.Helper()
		repo, pool, cleanup := setupUserRepoPGWithPool(t)
		txMgr := adapterpg.NewTxManager(pool)
		return repo, txMgr, cleanup
	}
}
