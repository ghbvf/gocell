package composition

import (
	"context"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// App is the assembled, ready-to-run GoCell application.  It is returned by
// [Builder.Build] and started by calling [App.Run].
//
// App is intentionally thin: it delegates directly to bootstrap.New(...).Run,
// keeping all assembly logic in [Builder.Build].
type App struct {
	clk  clock.Clock
	opts []bootstrap.Option
}

// Run starts the bootstrap lifecycle and blocks until the context is canceled
// or a fatal error occurs.
//
// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go —
// Manager.Start(ctx) error.
func (a *App) Run(ctx context.Context) error {
	return bootstrap.New(a.clk, a.opts...).Run(ctx)
}
