// Package assembly provides the CoreAssembly that orchestrates Cell
// lifecycle (register, start, stop, health).
//
// Design ref: uber-go/fx app.go, lifecycle.go
//   - FIFO Start / LIFO Stop
//   - Start 失败自动 rollback 已启动的 Cell
//   - Stop 尽力而为，合并错误
//   - 状态机防止重入
package assembly

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// assemblyState represents the lifecycle state of a CoreAssembly.
// ref: uber-go/fx lifecycle.go — stopped/starting/started/stopping
type assemblyState int

const (
	stateStopped  assemblyState = iota
	stateStarting               // Start() 正在执行
	stateStarted                // Start() 成功完成
	stateStopping               // Stop() 正在执行
)

// Config holds assembly-level configuration.
type Config struct {
	ID string
}

// CoreAssembly is the default Assembly implementation. It manages a set of
// Cells, starting them in registration order and stopping them in reverse.
type CoreAssembly struct {
	mu      sync.Mutex
	id      string
	cells   []cell.Cell
	cellMap map[string]cell.Cell
	state   assemblyState
}

// New creates a CoreAssembly with the given configuration.
func New(cfg Config) *CoreAssembly {
	return &CoreAssembly{
		id:      cfg.ID,
		cellMap: make(map[string]cell.Cell),
	}
}

// Register adds a Cell to the assembly. It returns an error if the Cell ID is
// empty, already registered, or the assembly has already been started.
func (a *CoreAssembly) Register(c cell.Cell) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state != stateStopped {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot register in state %d", a.id, a.state))
	}

	id := c.ID()
	if id == "" {
		return errcode.New(errcode.ErrValidationFailed, "cell ID must not be empty")
	}
	if _, exists := a.cellMap[id]; exists {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("duplicate cell ID: %q", id))
	}
	a.cells = append(a.cells, c)
	a.cellMap[id] = c
	return nil
}

// Start initialises and starts every registered Cell in registration order.
// Dependencies are built from all registered Cells.
//
// ref: uber-go/fx app.go — Start 出错后自动 rollback 已启动的 Cell（LIFO Stop）。
func (a *CoreAssembly) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.state != stateStopped {
		a.mu.Unlock()
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot start in state %d", a.id, a.state))
	}
	a.state = stateStarting
	a.mu.Unlock()

	deps := cell.Dependencies{
		Cells:     a.cellMap,
		Contracts: make(map[string]cell.Contract),
		Config:    make(map[string]any),
	}

	// Phase 1: Init all cells. If any fails, no cell has been Start'd yet.
	for _, c := range a.cells {
		if err := c.Init(ctx, deps); err != nil {
			a.mu.Lock()
			a.state = stateStopped
			a.mu.Unlock()
			return errcode.Wrap(errcode.ErrValidationFailed,
				fmt.Sprintf("assembly: init cell %q", c.ID()), err)
		}
	}

	// Phase 2: Start cells in order. On failure, rollback already-started cells.
	for i, c := range a.cells {
		if err := c.Start(ctx); err != nil {
			// Rollback: stop cells [0..i-1] in reverse order.
			for j := i - 1; j >= 0; j-- {
				if stopErr := a.cells[j].Stop(ctx); stopErr != nil {
					slog.Warn("rollback: failed to stop cell",
						"cell", a.cells[j].ID(), "error", stopErr)
				}
			}
			a.mu.Lock()
			a.state = stateStopped
			a.mu.Unlock()
			return errcode.Wrap(errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: start cell %q", c.ID()), err)
		}
	}

	a.mu.Lock()
	a.state = stateStarted
	a.mu.Unlock()
	return nil
}

// Stop stops every registered Cell in reverse registration order. If multiple
// Cells fail, Stop continues and returns the first error encountered.
// Stop is only allowed from the Started state; calling Stop in any other state
// is a no-op.
//
// ref: uber-go/fx app.go — Stop 尽力而为，不因某个 hook 失败而中止。
func (a *CoreAssembly) Stop(ctx context.Context) error {
	a.mu.Lock()
	if a.state != stateStarted {
		a.mu.Unlock()
		return nil // Only allow Stop from Started state.
	}
	a.state = stateStopping
	a.mu.Unlock()

	var firstErr error
	for i := len(a.cells) - 1; i >= 0; i-- {
		if err := a.cells[i].Stop(ctx); err != nil {
			if firstErr == nil {
				firstErr = errcode.Wrap(errcode.ErrLifecycleInvalid,
					fmt.Sprintf("assembly: stop cell %q", a.cells[i].ID()), err)
			}
		}
	}

	a.mu.Lock()
	a.state = stateStopped
	a.mu.Unlock()
	return firstErr
}

// Health returns the HealthStatus of every registered Cell, keyed by Cell ID.
func (a *CoreAssembly) Health() map[string]cell.HealthStatus {
	result := make(map[string]cell.HealthStatus, len(a.cells))
	for _, c := range a.cells {
		result[c.ID()] = c.Health()
	}
	return result
}

// StartWithConfig is like Start but injects the given config map into
// Dependencies.Config before initialising cells.
func (a *CoreAssembly) StartWithConfig(ctx context.Context, cfgMap map[string]any) error {
	a.mu.Lock()
	if a.state != stateStopped {
		a.mu.Unlock()
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot start in state %d", a.id, a.state))
	}
	a.state = stateStarting
	a.mu.Unlock()

	deps := cell.Dependencies{
		Cells:     a.cellMap,
		Contracts: make(map[string]cell.Contract),
		Config:    cfgMap,
	}

	for _, c := range a.cells {
		if err := c.Init(ctx, deps); err != nil {
			a.mu.Lock()
			a.state = stateStopped
			a.mu.Unlock()
			return errcode.Wrap(errcode.ErrValidationFailed,
				fmt.Sprintf("assembly: init cell %q", c.ID()), err)
		}
	}

	for i, c := range a.cells {
		if err := c.Start(ctx); err != nil {
			for j := i - 1; j >= 0; j-- {
				if stopErr := a.cells[j].Stop(ctx); stopErr != nil {
					slog.Warn("rollback: failed to stop cell",
						"cell", a.cells[j].ID(), "error", stopErr)
				}
			}
			a.mu.Lock()
			a.state = stateStopped
			a.mu.Unlock()
			return errcode.Wrap(errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: start cell %q", c.ID()), err)
		}
	}

	a.mu.Lock()
	a.state = stateStarted
	a.mu.Unlock()
	return nil
}

// CellIDs returns the IDs of all registered cells in registration order.
func (a *CoreAssembly) CellIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, len(a.cells))
	for i, c := range a.cells {
		ids[i] = c.ID()
	}
	return ids
}

// Cell returns the registered Cell with the given ID, or nil if not found.
func (a *CoreAssembly) Cell(id string) cell.Cell {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cellMap[id]
}
