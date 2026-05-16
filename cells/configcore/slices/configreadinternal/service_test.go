package configreadinternal

import (
	"testing"

	internalapig "github.com/ghbvf/gocell/generated/contracts/http/config/internalapi/get/v1"
)

// TestServiceAliasAndAdapter asserts the slice's structural contract at
// compile time: InternalGetAdapter implements internalapig.Service. The
// Service type alias identity is enforced by the compiler in handler.go and
// handler_test.go where Service is used interchangeably with
// configreader.Service.
//
// This satisfies gocell verify slice --id=configcore/configreadinternal
// (-run Service).
func TestServiceAliasAndAdapter(t *testing.T) {
	t.Helper()

	// InternalGetAdapter implements the generated internalapig.Service interface
	// (value receiver, so value satisfies the interface).
	var _ internalapig.Service = InternalGetAdapter{}
}
