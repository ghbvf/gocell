package command

import (
	"fmt"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	kcommand "github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// DiscoverQueueRegistrars injects q into every cell that implements
// kernel/command.QueueRegistrar. It mirrors the optional-capability discovery
// style now collapsed into cell.Registrar while keeping command queue
// ownership in the composition root.
//
// q is a kcommand.QueueWithScanner (Queue + ActiveScanner): the registrar
// contract requires the composite, so callers must supply a queue that is also
// an ActiveScanner — enforced at compile time (QUEUE-REGISTRAR-SCANNER-REQUIRED-01).
func DiscoverQueueRegistrars(cells []cell.Cell, q kcommand.QueueWithScanner) (int, error) {
	// q is an interface dependency: a bare `q == nil` misses a typed-nil
	// (e.g. a nil *PGCommandQueue boxed into QueueWithScanner), which would be
	// injected as a "successful" but unusable queue. IsNilInterface is the
	// repo's construction-boundary guard (same as validateRequired's codegen).
	if validation.IsNilInterface(q) {
		return 0, fmt.Errorf("runtime/command: queue must not be nil")
	}

	count := 0
	for _, c := range cells {
		if c == nil {
			continue
		}
		registrar, ok := c.(kcommand.QueueRegistrar)
		if !ok {
			continue
		}
		registrar.RegisterCommandQueue(q)
		count++
	}
	return count, nil
}

// DiscoverQueueRegistrarsInAssembly injects q into QueueRegistrar cells in
// assembly registration order.
func DiscoverQueueRegistrarsInAssembly(asm *assembly.CoreAssembly, q kcommand.QueueWithScanner) (int, error) {
	if asm == nil {
		return 0, fmt.Errorf("runtime/command: assembly must not be nil")
	}
	cells := make([]cell.Cell, 0, len(asm.CellIDs()))
	for _, id := range asm.CellIDs() {
		cells = append(cells, asm.Cell(id))
	}
	return DiscoverQueueRegistrars(cells, q)
}
