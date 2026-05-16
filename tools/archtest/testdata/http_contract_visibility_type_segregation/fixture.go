//go:build archtest_fixture

// Package http_contract_visibility_type_segregation is an archtest RED fixture
// for HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01.
//
// It declares a MonolithicService struct that simultaneously implements:
//   - enqueue.Service (public-path contract)
//   - list.Service (internal-path contract via internalapi path)
//
// This mirrors the pre-F1' devicecmd.Service monolith that had all 6 contract
// interface assertions. The fixture lives under testdata/ and is excluded from
// the main module build by the archtest_fixture build tag.
//
// The correct GREEN form (after F1') uses per-slice Adapter types:
// EnqueueAdapter{S *Service} (public) and InternalListAdapter{S *Service}
// (internal), each implementing exactly one contract Service interface.
package http_contract_visibility_type_segregation

import (
	"context"

	ackcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/ack/v1"
	dequeuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/dequeue/v1"
	enqueuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/enqueue/v1"
	extendleasecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/extend-lease/v1"
	reportcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/report/v1"
	listcontract "github.com/ghbvf/gocell/generated/contracts/http/internalapi/devicecommands/list/v1"
)

// MonolithicService is the RED fixture: a single struct that simultaneously
// implements 5 public-path and 1 internal-path generated Service interfaces.
// This is the pre-F1' devicecmd.Service shape that HTTP-CONTRACT-VISIBILITY-
// TYPE-SEGREGATION-01 is designed to catch.
//
// Compile-time assertions (mirrors the old interface assertion block):
var (
	_ enqueuecontract.Service     = (*MonolithicService)(nil)
	_ dequeuecontract.Service     = (*MonolithicService)(nil)
	_ reportcontract.Service      = (*MonolithicService)(nil)
	_ ackcontract.Service         = (*MonolithicService)(nil)
	_ extendleasecontract.Service = (*MonolithicService)(nil)
	_ listcontract.Service        = (*MonolithicService)(nil)
)

// MonolithicService implements all 6 contract Service interfaces — the
// trust-boundary violation that HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01
// prevents in production code.
type MonolithicService struct{}

func (m *MonolithicService) Enqueue(ctx context.Context, req *enqueuecontract.Request) (enqueuecontract.EnqueueResponseObject, error) {
	return nil, nil
}

func (m *MonolithicService) Dequeue(ctx context.Context, req *dequeuecontract.Request) (dequeuecontract.DequeueResponseObject, error) {
	return nil, nil
}

func (m *MonolithicService) Report(ctx context.Context, req *reportcontract.Request) (reportcontract.ReportResponseObject, error) {
	return nil, nil
}

func (m *MonolithicService) Ack(ctx context.Context, req *ackcontract.Request) (ackcontract.AckResponseObject, error) {
	return nil, nil
}

func (m *MonolithicService) ExtendLease(ctx context.Context, req *extendleasecontract.Request) (extendleasecontract.ExtendLeaseResponseObject, error) {
	return nil, nil
}

func (m *MonolithicService) List(ctx context.Context, req *listcontract.Request) (listcontract.ListResponseObject, error) {
	return nil, nil
}
