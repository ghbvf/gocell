package devicecommandinternal

import (
	"testing"

	listcontract "github.com/ghbvf/gocell/generated/contracts/http/internalapi/devicecommands/list/v1"
)

// TestServiceAdapterImplementsInternalContract asserts the slice's structural
// contract at compile time: InternalListAdapter implements only the
// internal-path generated listcontract.Service interface. The Service type
// alias identity is enforced by the compiler wherever Service is used
// interchangeably with devicecmd.Service in handler.go / handler_test.go.
//
// This satisfies gocell verify slice --id=devicecell/devicecommandinternal
// (-run Service) and pins the type-level public/internal segregation: this
// adapter implements only an internal-path contract interface (see
// HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01 archtest).
func TestServiceAdapterImplementsInternalContract(t *testing.T) {
	t.Helper()

	var _ listcontract.Service = InternalListAdapter{}
}
