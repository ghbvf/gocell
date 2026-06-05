// Package democell implements the democell Cell for the demo example — a
// minimal hello-world that serves GET /api/v1/hello with no external
// dependencies (no database, no events, no saga). It is the smallest runnable
// GoCell cell and exists to show newcomers the Cell → Slice → Contract → handler
// shape end to end.
package democell

import (
	"context"
	"fmt"
	"log/slog"

	helloslice "github.com/ghbvf/gocell/examples/demo/cells/democell/slices/hello"
	hellov1 "github.com/ghbvf/gocell/generated/contracts/http/demo/hello/v1"
	"github.com/ghbvf/gocell/kernel/cell"
)

// Option configures a DemoCell.
type Option func(*DemoCell)

// WithLogger sets the structured logger. A nil value is a silent noop.
func WithLogger(l *slog.Logger) Option {
	return func(c *DemoCell) {
		if l != nil {
			c.logger = l
		}
	}
}

// DemoCell is the democell Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type DemoCell struct {
	*cell.BaseCell

	logger *slog.Logger

	// helloHandler is built in initInternal and mounted on the PrimaryListener
	// by the cellgen-generated RouteGroup in cell_gen.go, driven by the marker
	// below (the marker prefix must start the comment line; keep prose off it).
	// +slice:route:slice=hello,subPath=/hello
	helloHandler *hellov1.Handler

	// helloSvc holds the hello slice service for slice metadata wiring.
	helloSvc *helloslice.Service
}

// NewDemoCell creates a new DemoCell.
func NewDemoCell(opts ...Option) *DemoCell {
	c := &DemoCell{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// initInternal is the K#04 codegen escape hatch: it constructs the hello slice
// service + handler and registers the slice metadata. Route mounting is
// generated into cell_gen.go from the +slice:route marker above; cell_gen.go::Init
// calls this hook after BaseCell.Init and before the generated RouteGroup block.
//
// ctx and reg are part of the fixed K#04 initInternal contract; this minimal
// cell needs neither (it registers no readiness probe and uses no request ctx).
func (c *DemoCell) initInternal(_ context.Context, _ cell.Registrar) error {
	svc, err := helloslice.NewService()
	if err != nil {
		return fmt.Errorf("democell: build hello service: %w", err)
	}
	c.helloSvc = svc
	c.helloHandler = hellov1.NewHandler(helloslice.NewHandler(svc))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(helloslice.SliceMetadata()))
	return nil
}
