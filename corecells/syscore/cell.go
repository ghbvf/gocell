// Package syscore implements the syscore Cell: a stateless platform
// system/observability boundary cell (#1860). Its sole slice, healthread, serves
// the aggregated cell-health contract http.admin.health.cells.v1, and systemread
// serves process system metadata via http.admin.system.v1 on the primary
// listener. Both routes are gated by the system:read permission. The cross-cell
// health data and system metadata are framework-provided read views injected into
// request context by bootstrap — syscore never imports a sibling cell.
//
// It is L1 (the minimum for an HTTP contract provider; TOPO-05 reserves L0 for
// inbound webhook-receive) and persists nothing; schema.primary reserves the
// cell_syscore namespace as the framework marker of a real boundary cell.
package syscore

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/corecells/syscore/slices/healthread"
	"github.com/ghbvf/gocell/corecells/syscore/slices/systemread"
	"github.com/ghbvf/gocell/framework/kernel/cell"
)

// SysCore is the platform system/observability cell. It embeds BaseCell and owns
// a single HTTP handler; Init is generated in cell_gen.go and calls initInternal.
// The cell owns the /api/v1/admin prefix (distinct per-cell route ownership —
// configcore owns the bare /api/v1, so syscore must claim its own segment, like
// auditcore=/api/v1/audit and accesscore=/api/v1/access).
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/admin
type SysCore struct {
	*cell.BaseCell

	// +slice:route:slice=healthread,subPath=/health
	healthHandler *healthread.Handler

	// +slice:route:slice=systemread,subPath=/system
	systemHandler *systemread.Handler
}

// New constructs the syscore cell. It takes no dependencies: the slice is
// stateless and reads the runtime HealthView from request context at serve time.
func New() *SysCore {
	return &SysCore{BaseCell: cell.MustNewBaseCell(loadCellMetadata())}
}

// initInternal is the K#04 hand-written init hook invoked by the generated Init
// after BaseCell.Init and before the generated route-group mount. It constructs
// the slice handler the generated route group references. No external I/O, no
// goroutines, fail-fast (cell-patterns.md §Init fail-fast) — here construction
// cannot fail, so it returns nil.
func (c *SysCore) initInternal(_ context.Context, _ cell.Registrar) error {
	svc, err := healthread.NewService()
	if err != nil {
		return fmt.Errorf("syscore: build healthread service: %w", err)
	}
	c.healthHandler = healthread.NewHandler(svc)

	systemSvc, err := systemread.NewService()
	if err != nil {
		return fmt.Errorf("syscore: build systemread service: %w", err)
	}
	c.systemHandler = systemread.NewHandler(systemSvc, cellHTTPResolver)
	return nil
}
