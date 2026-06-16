package softca

import "github.com/ghbvf/gocell/framework/kernel/clock"

// NewSoftCA wires a [Signer] and a [RevocationStore] that SHARE one issuance
// [Ledger] — the correct-by-construction entry point. Revocation only sees the
// certificates the signer issued when both sides hold the SAME Ledger; this
// constructor makes that the path of least resistance so a caller cannot
// accidentally hand the two halves two different ledgers (which would silently
// make every Revoke return not-found). Reach for the individual [NewSigner] /
// [NewRevocationStore] only when you deliberately need them apart and have
// threaded the same Ledger through both yourself.
//
//	ca, err := softca.NewDevCA(clk)            // or NewFileCA(clk, dir)
//	signer, revStore, err := softca.NewSoftCA(clk, ca, softca.NewMemLedger())
func NewSoftCA(clk clock.Clock, ca *CA, ledger Ledger) (*Signer, *RevocationStore, error) {
	signer, err := NewSigner(clk, ca, ledger)
	if err != nil {
		return nil, nil, err
	}
	revStore, err := NewRevocationStore(clk, ca, ledger)
	if err != nil {
		return nil, nil, err
	}
	return signer, revStore, nil
}
