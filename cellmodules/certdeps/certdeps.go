package certdeps

import (
	"fmt"

	"github.com/ghbvf/gocell/adapters/softca"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// CertDeps is the resolved cert-signing dependency set a composition root wires
// into a device-identity cell: the issuing Signer and the matching
// RevocationStore. softca.NewSoftCA hands both halves the SAME issuance ledger,
// so revocation sees exactly the certificates the signer minted (correct by
// construction).
type CertDeps struct {
	Signer          certsigning.Signer
	RevocationStore certsigning.RevocationStore
}

// Resolve maps topo to the cert-signing backend. clk is the mandatory positional
// clock. See the package doc for the selection matrix and the postgres
// fail-closed invariant (CERTDEPS-INMEM-FUNNEL-01).
func Resolve(clk clock.Clock, topo bootstrap.Topology) (CertDeps, error) {
	clock.MustHaveClock(clk, "certdeps.Resolve")

	if topo.StorageBackend() == bootstrap.StorageBackendPostgres {
		// Fail-closed: postgres promises durability, but no durable signing CA or
		// issuance-ledger backend exists yet to wire here. The only available CA is
		// the dev soft-CA, whose trust anchor rotates on restart and whose
		// in-memory ledger loses issuance/revocation state across restart, so
		// serving it under a postgres topology would silently break the durability
		// the topology promises. Refuse rather than degrade. (Const literal message
		// per MESSAGE-CONST-LITERAL-01.)
		return CertDeps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"certdeps: postgres topology requires a durable signing CA and issuance "+
				"ledger, which are not yet wired; refusing to silently serve a dev "+
				"soft-CA whose trust anchor rotates on restart and whose in-memory "+
				"ledger loses issuance and revocation state across restart")
	}

	// demo / memory: ephemeral two-tier dev CA + in-memory issuance ledger.
	// Reaching here means StorageBackend() != postgres; "memory" is the only other
	// value bootstrap.Topology.validate() admits (a zero-value Topology normalizes
	// to "memory"), so this branch is not a silent catch-all for an unvalidated
	// backend string.
	ca, err := softca.NewDevCA(clk)
	if err != nil {
		return CertDeps{}, fmt.Errorf("certdeps: build dev CA: %w", err)
	}
	signer, revStore, err := softca.NewSoftCA(clk, ca, softca.NewMemLedger())
	if err != nil {
		return CertDeps{}, fmt.Errorf("certdeps: build soft CA: %w", err)
	}
	return CertDeps{Signer: signer, RevocationStore: revStore}, nil
}
