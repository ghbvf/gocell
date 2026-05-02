package command

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kcommand "github.com/ghbvf/gocell/kernel/command"
)

// DiscoverQueueRegistrars injects q into every cell that implements
// kernel/command.QueueRegistrar. It mirrors the optional-capability discovery
// style now collapsed into cell.Registry while keeping command queue
// ownership in the composition root.
func DiscoverQueueRegistrars(cells []cell.Cell, q kcommand.Queue) (int, error) {
	if q == nil {
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
func DiscoverQueueRegistrarsInAssembly(asm *assembly.CoreAssembly, q kcommand.Queue) (int, error) {
	if asm == nil {
		return 0, fmt.Errorf("runtime/command: assembly must not be nil")
	}
	cells := make([]cell.Cell, 0, len(asm.CellIDs()))
	for _, id := range asm.CellIDs() {
		cells = append(cells, asm.Cell(id))
	}
	return DiscoverQueueRegistrars(cells, q)
}
