//go:build integration

package integration

import "testing"

// TestJAuditlogintrailHashChain implements journeys/J-auditlogintrail.yaml
// passCriteria "审计记录写入 hash chain" — checkRef
// journey.J-auditlogintrail.hash-chain.
//
// WAVE 1 RED placeholder — Wave 2 will replace this body with the
// docker-free hash-chain integrity assertion: drive the same subscription as
// event-consume, then assert ledger.Store.Tail returns SeqNo=1 and
// ledger.Store.Verify(1, tail.SeqNo) returns valid=true.
func TestJAuditlogintrailHashChain(t *testing.T) {
	t.Fatal("RED: J-auditlogintrail.hash-chain not implemented")
}
