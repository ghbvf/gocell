// cell_lifecycle.go implements the optional cell-level lifecycle hooks
// (cell.AfterStarter / cell.BeforeStopper). The assembly discovers these via
// type assertion and invokes them around BaseCell.Start/Stop. ConfigCore uses
// them solely to bind the configsubscribe tombstone-GC goroutine to the cell
// lifecycle (start after the cell is up, drain before it stops) — no goroutine
// leak, no persistence. See docs/plans .../035 PR-CFG-CACHE-LIFECYCLE.
package configcore

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

var (
	_ cell.AfterStarter  = (*ConfigCore)(nil)
	_ cell.BeforeStopper = (*ConfigCore)(nil)
)

// AfterStart launches the configsubscribe tombstone-GC sweep. Runs after
// BaseCell.Start succeeds (BeforeStart must not acquire resources needing
// cleanup; the GC goroutine needs cleanup, so it starts here).
func (c *ConfigCore) AfterStart(context.Context) error {
	if c.subscribeSvc != nil {
		c.subscribeSvc.StartTombstoneGC()
	}
	return nil
}

// BeforeStop signals the tombstone-GC goroutine and waits for it to drain,
// honoring ctx for the shutdown deadline. Idempotent / safe if never started.
func (c *ConfigCore) BeforeStop(ctx context.Context) error {
	if c.subscribeSvc != nil {
		return c.subscribeSvc.StopTombstoneGC(ctx)
	}
	return nil
}
