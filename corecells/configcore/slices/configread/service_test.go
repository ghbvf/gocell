package configread

import (
	"testing"

	configget "github.com/ghbvf/gocell/generated/contracts/http/config/get/v1"
	configlist "github.com/ghbvf/gocell/generated/contracts/http/config/list/v1"
)

// TestServiceAliasAndAdapters asserts the slice's structural contract at
// compile time: GetAdapter implements configget.Service and ListAdapter
// implements configlist.Service. The Service type alias identity is enforced
// by the compiler in handler.go and handler_test.go where Service is used
// interchangeably with configreader.Service.
//
// This satisfies gocell verify slice --id=configcore/configread (-run Service).
func TestServiceAliasAndAdapters(t *testing.T) {
	t.Helper()

	// GetAdapter implements the generated configget.Service interface
	// (value receiver, so value satisfies the interface).
	var _ configget.Service = GetAdapter{}

	// ListAdapter implements the generated configlist.Service interface
	// (value receiver, so value satisfies the interface).
	var _ configlist.Service = ListAdapter{}
}
