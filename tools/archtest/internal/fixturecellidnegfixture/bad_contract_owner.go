//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/framework/kernel/metadata"

// BadContractOwner uses a bare string literal in ContractMeta.OwnerCell —
// must be flagged by A1 (direct field position).
var BadContractOwner = &metadata.ContractMeta{
	ID:        "http.myservice.v1",
	Kind:      "http",
	OwnerCell: "bareliteralowner",
}
