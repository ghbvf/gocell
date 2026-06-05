// Package hooks implements the hooks Cell for the webhookdemo example: a minimal
// inbound-webhook receiver. It demonstrates the GoCell webhook path end-to-end —
// a contract (kind: webhook, direction: inbound), a slice with
// contractUsages[role=webhook-receive], the cellgen-derived
// reg.RegisterWebhookReceiver call (cell_gen.go), and an assembly that mounts the
// receiver on the dedicated cell.WebhookListener (run.go).
//
// Consistency: the cell is declared L1 in cell.yaml — not because it runs a
// transaction (HandleEvent only decodes + logs), but because TOPO-05 forbids an
// L0 cell from being a contract provider/consumer, and an inbound webhook
// contract's provider is its ownerCell. The eventreceive slice itself is L0. The
// cell holds no outbox/txManager; HMAC verification, the timestamp window, and
// idempotency are the runtime receiver's job, not the cell's.
package hooks

import (
	"context"
	"log/slog"

	eventreceive "github.com/ghbvf/gocell/examples/webhookdemo/cells/hooks/slices/eventreceive"
	"github.com/ghbvf/gocell/kernel/cell"
)

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

// HooksCell is the hooks Cell implementation — an inbound webhook receiver.
type HooksCell struct {
	*cell.BaseCell
	logger          *slog.Logger
	eventreceiveSvc *eventreceive.Service
}

// Option configures a HooksCell.
type Option func(*HooksCell)

// WithLogger sets the structured logger. A nil logger is ignored.
func WithLogger(l *slog.Logger) Option {
	return func(c *HooksCell) {
		if l != nil {
			c.logger = l
		}
	}
}

// NewHooksCell creates a new HooksCell. loadCellMetadata() is generated into
// cell_gen.go from cell.yaml.
func NewHooksCell(opts ...Option) *HooksCell {
	c := &HooksCell{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// initInternal is the K#04 codegen escape hatch: hand-written init that
// cell_gen.go::Init calls after BaseCell.Init and before the generated
// reg.RegisterWebhookReceiver block. It constructs the eventreceive slice
// service (whose HandleEvent the generated block binds) and registers the slice.
//
// cellgen resolves the generated handler expression c.eventreceiveSvc.HandleEvent
// by matching the field whose pointer type's package name == the slice id
// "eventreceive".
//
// ctx and reg are part of the codegen-fixed signature (cell_gen.go calls
// initInternal(ctx, reg)); this minimal receiver cell needs neither.
//
//nolint:unparam // ctx/reg are codegen contract params; this receiver uses neither
func (c *HooksCell) initInternal(ctx context.Context, reg cell.Registrar) error {
	c.eventreceiveSvc = eventreceive.NewService(eventreceive.WithLogger(c.logger))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(eventreceive.SliceMetadata()))
	return nil
}
