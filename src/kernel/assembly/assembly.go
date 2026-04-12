// Package assembly provides the CoreAssembly that orchestrates Cell
// lifecycle (register, start, stop, health).
//
// Design ref: uber-go/fx app.go, lifecycle.go
//   - FIFO Start / LIFO Stop
//   - Start 失败自动 rollback 已启动的 Cell
//   - Stop 尽力而为，合并错误
//   - 状态机防止重入
//
// ref: go-kratos/kratos app.go — BeforeStart/AfterStart/BeforeStop/AfterStop
// ref: uber-go/fx lifecycle.go — FIFO Start, LIFO Stop, rollback on failure
package assembly

import (
	"context"
	"errors"
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

	if c == nil {
		return errcode.New(errcode.ErrValidationFailed, "cell must not be nil")
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
	return a.startInternal(ctx, nil)
}

// Stop stops every registered Cell in reverse registration order. If multiple
// Cells fail, Stop continues and returns all errors joined via errors.Join.
// Stop is only allowed from the Started state; calling Stop in any other state
// is a no-op.
//
// For each cell, the sequence is: BeforeStop → Stop → AfterStop.
// All three phases are best-effort — errors are accumulated, never abort.
//
// ref: uber-go/fx app.go — Stop 尽力而为，不因某个 hook 失败而中止。
// ref: go-kratos/kratos app.go — BeforeStop/AfterStop hooks around server.Stop
func (a *CoreAssembly) Stop(ctx context.Context) error {
	a.mu.Lock()
	if a.state != stateStarted {
		a.mu.Unlock()
		return nil // Only allow Stop from Started state.
	}
	a.state = stateStopping
	a.mu.Unlock()

	var errs []error
	for i := len(a.cells) - 1; i >= 0; i-- {
		errs = append(errs, a.stopCellWithHooks(ctx, a.cells[i])...)
	}

	a.mu.Lock()
	a.state = stateStopped
	a.mu.Unlock()
	return errors.Join(errs...)
}

// stopCellWithHooks executes BeforeStop → Stop → AfterStop for a single cell.
// All phases are best-effort: errors are accumulated but never abort the sequence.
// Logging is handled here — callers should not log the returned errors again.
//
// ref: runtime/worker/periodic.go runSafe — panic recovery pattern
func (a *CoreAssembly) stopCellWithHooks(ctx context.Context, c cell.Cell) []error {
	var errs []error
	if bs, ok := c.(cell.BeforeStopper); ok {
		if err := callHookSafe(func() error { return bs.BeforeStop(ctx) }); err != nil {
			slog.Warn("lifecycle: BeforeStop failed",
				slog.String("cell", c.ID()), slog.Any("error", err))
			errs = append(errs, errcode.Wrap(errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: BeforeStop cell %q", c.ID()), err))
		}
	}
	if err := c.Stop(ctx); err != nil {
		errs = append(errs, errcode.Wrap(errcode.ErrLifecycleInvalid,
			fmt.Sprintf("assembly: stop cell %q", c.ID()), err))
	}
	if as, ok := c.(cell.AfterStopper); ok {
		if err := callHookSafe(func() error { return as.AfterStop(ctx) }); err != nil {
			slog.Warn("lifecycle: AfterStop failed",
				slog.String("cell", c.ID()), slog.Any("error", err))
			errs = append(errs, errcode.Wrap(errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: AfterStop cell %q", c.ID()), err))
		}
	}
	return errs
}

// Health returns the HealthStatus of every registered Cell, keyed by Cell ID.
func (a *CoreAssembly) Health() map[string]cell.HealthStatus {
	a.mu.Lock()
	snapshot := make([]cell.Cell, len(a.cells))
	copy(snapshot, a.cells)
	a.mu.Unlock()

	result := make(map[string]cell.HealthStatus, len(snapshot))
	for _, c := range snapshot {
		result[c.ID()] = c.Health()
	}
	return result
}

// StartWithConfig is like Start but injects the given config map into
// Dependencies.Config before initialising cells.
func (a *CoreAssembly) StartWithConfig(ctx context.Context, cfgMap map[string]any) error {
	return a.startInternal(ctx, cfgMap)
}

// startInternal is the shared implementation for Start and StartWithConfig.
// If cfgMap is nil an empty map is used for Dependencies.Config.
//
// ref: uber-go/fx app.go — Start 出错后自动 rollback 已启动的 Cell（LIFO Stop）。
func (a *CoreAssembly) startInternal(ctx context.Context, cfgMap map[string]any) error {
	a.mu.Lock()
	if a.state != stateStopped {
		a.mu.Unlock()
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot start in state %d", a.id, a.state))
	}
	a.state = stateStarting
	a.mu.Unlock()

	if cfgMap == nil {
		cfgMap = make(map[string]any)
	}

	deps := cell.Dependencies{
		Config: cfgMap,
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

	// Phase 2: Start cells in order with lifecycle hooks.
	// For each cell: BeforeStart → Start → AfterStart.
	// On any failure, rollback already-started cells in reverse (LIFO).
	//
	// ref: go-kratos/kratos app.go — BeforeStart/AfterStart hooks around server.Start
	// ref: uber-go/fx app.go — Start failure triggers LIFO rollback of started hooks
	for i, c := range a.cells {
		if err := a.startCellWithHooks(ctx, c, i); err != nil {
			return err
		}
	}

	a.mu.Lock()
	a.state = stateStarted
	a.mu.Unlock()
	return nil
}

// startCellWithHooks executes BeforeStart → Start → AfterStart for a single
// cell at index i. On any failure it rolls back cells [0..i-1] (and cell i
// itself if Start already succeeded) and transitions the assembly to stopped.
func (a *CoreAssembly) startCellWithHooks(ctx context.Context, c cell.Cell, i int) error {
	// BeforeStart hook (optional).
	if bs, ok := c.(cell.BeforeStarter); ok {
		slog.Info("lifecycle: BeforeStart", slog.String("cell", c.ID()))
		if err := callHookSafe(func() error { return bs.BeforeStart(ctx) }); err != nil {
			a.rollbackCells(ctx, i-1)
			return a.failStart(c.ID(), "BeforeStart", err)
		}
	}

	// Core Start.
	if err := c.Start(ctx); err != nil {
		a.rollbackCells(ctx, i-1)
		return a.failStart(c.ID(), "start", err)
	}

	// AfterStart hook (optional).
	// If this fails, the cell itself must be stopped because its Start
	// already succeeded — resources may have been acquired.
	if as, ok := c.(cell.AfterStarter); ok {
		slog.Info("lifecycle: AfterStart", slog.String("cell", c.ID()))
		if err := callHookSafe(func() error { return as.AfterStart(ctx) }); err != nil {
			// Stop this cell first — its Start already succeeded.
			a.stopCellWithHooks(ctx, c) //nolint:errcheck // best-effort, logged inside
			a.rollbackCells(ctx, i-1)
			return a.failStart(c.ID(), "AfterStart", err)
		}
	}
	return nil
}

// failStart transitions the assembly to stopped and returns a wrapped error.
func (a *CoreAssembly) failStart(cellID, phase string, err error) error {
	a.mu.Lock()
	a.state = stateStopped
	a.mu.Unlock()
	return errcode.Wrap(errcode.ErrLifecycleInvalid,
		fmt.Sprintf("assembly: %s cell %q", phase, cellID), err)
}

// rollbackCells stops cells [0..upTo] in reverse order using stopCellWithHooks.
// All errors are logged inside stopCellWithHooks (best-effort, never abort).
func (a *CoreAssembly) rollbackCells(ctx context.Context, upTo int) {
	for j := upTo; j >= 0; j-- {
		a.stopCellWithHooks(ctx, a.cells[j]) //nolint:errcheck // best-effort, logged inside
	}
}

// callHookSafe invokes fn with panic recovery. If fn panics, the panic is
// converted to an error. This protects the assembly from a single misbehaving
// cell crashing the entire process.
//
// ref: runtime/worker/periodic.go runSafe — same panic-to-error pattern
// ref: runtime/eventrouter/router.go — recover in subscription goroutine
func callHookSafe(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("lifecycle hook panicked: %v", r)
		}
	}()
	return fn()
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
