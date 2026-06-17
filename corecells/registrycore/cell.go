// Package registrycore implements the registrycore Cell: the runtime contract
// registry's carrier cell (303-US4, #2235), a platform control-plane cell beside
// accesscore / auditcore / configcore (dogfooding — the registry is itself a
// Cell). Its two slices serve the submit and list HTTP contracts over the in-mem
// kernel ContractRegistrar (303-US2, #2233): registrywrite serves
// http.registry.contract.submit.v1 (POST), registryread serves
// http.registry.contract.list.v1 (GET), both gated by a registry:* permission.
//
// US4 is the skeleton: the cell-scoped ContractRegistrar is process-local and
// tenant-agnostic, so submit/list are the minimal usable submission surface but
// persist nothing durable. The durable contract_registrations store (behind a
// ports.Registry interface) is US5; the governance gate, audit/events, and the
// cellmodule + corebundle composition (with the PDP baseline grant) are US6.
// registrycore never imports a sibling cell.
package registrycore

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/corecells/registrycore/slices/registryread"
	"github.com/ghbvf/gocell/corecells/registrycore/slices/registrywrite"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
)

// RegistryCore is the platform runtime contract-registry cell. It embeds BaseCell
// and owns the two slice handlers; Init is generated in cell_gen.go and calls
// initInternal. The cell owns the /api/v1/registry prefix (distinct per-cell route
// ownership, like auditcore=/api/v1/audit and accesscore=/api/v1/access).
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/registry
type RegistryCore struct {
	*cell.BaseCell

	// clk stamps registration timestamps inside the shared ContractRegistrar.
	clk clock.Clock

	// +slice:route:slice=registrywrite,subPath=/contracts
	writeHandler *registrywrite.Handler
	// +slice:route:slice=registryread,subPath=/contracts
	readHandler *registryread.Handler
}

// New constructs the registrycore cell. clk is the positional clock dependency
// (clock.Clock convention) the shared ContractRegistrar uses to stamp event
// timestamps.
func New(clk clock.Clock) *RegistryCore {
	clock.MustHaveClock(clk, "registrycore.New")
	return &RegistryCore{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		clk:      clk,
	}
}

// initInternal is the K#04 hand-written init hook invoked by the generated Init
// after BaseCell.Init and before the generated route-group mount. It builds the
// single cell-scoped in-mem ContractRegistrar (#2233) shared by both slice
// services — so a submit records a registration the list slice reads back — and
// the slice handlers the generated route group references. No external I/O, no
// goroutines, fail-fast (cell-patterns.md §Init fail-fast).
func (c *RegistryCore) initInternal(_ context.Context, _ cell.Registrar) error {
	registrar := registry.NewContractRegistrar(c.clk)

	writeSvc, err := registrywrite.NewService(c.clk, registrar)
	if err != nil {
		return fmt.Errorf("registrycore: build registrywrite service: %w", err)
	}
	readSvc, err := registryread.NewService(registrar)
	if err != nil {
		return fmt.Errorf("registrycore: build registryread service: %w", err)
	}
	c.writeHandler = registrywrite.NewHandler(writeSvc)
	c.readHandler = registryread.NewHandler(readSvc)
	return nil
}
