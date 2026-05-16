package devicecommand

import (
	"testing"

	ackcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/ack/v1"
	dequeuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/dequeue/v1"
	enqueuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/enqueue/v1"
	extendleasecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/extend-lease/v1"
	reportcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/report/v1"
)

// TestServiceAdaptersImplementPublicContracts asserts the slice's structural
// contract at compile time: each public command adapter implements exactly its
// generated contract Service interface. The Service type alias identity is
// enforced by the compiler wherever Service is used interchangeably with
// devicecmd.Service in handler.go / handler_test.go.
//
// This satisfies gocell verify slice --id=devicecell/devicecommand (-run Service)
// and pins the type-level public/internal segregation: these adapters
// implement only public-path contract interfaces (see
// HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01 archtest).
func TestServiceAdaptersImplementPublicContracts(t *testing.T) {
	t.Helper()

	var _ enqueuecontract.Service = EnqueueAdapter{}
	var _ dequeuecontract.Service = DequeueAdapter{}
	var _ reportcontract.Service = ReportAdapter{}
	var _ ackcontract.Service = AckAdapter{}
	var _ extendleasecontract.Service = ExtendLeaseAdapter{}
}
