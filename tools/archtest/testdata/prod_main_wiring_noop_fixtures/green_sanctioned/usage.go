//go:build archtest_fixture

// Package greensanctioned is a GREEN fixture for PROD-MAIN-WIRING-NOOP-REJECT-01:
// a composition-root package that wires events ONLY through sanctioned paths —
// the sealed demo funnels (outbox.DemoCellEmitter / outbox.DemoCellTxManager),
// the intended-L4 direct emitter ctor (outbox.NewDirectCellEmitter), the durable
// resolver (outbox.ResolveEmitter), and the in-process event bus
// (runtime/eventbus.New, the sole sanctioned publisher per option A). None is a
// forbidden raw-noop sink, so the rule must emit ZERO diagnostics (empty golden).
// This proves the detector does not false-positive on the sanctioned funnels it
// is meant to push raw noop through.
package greensanctioned

import (
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
)

var (
	_ = outbox.DemoCellEmitter()    // sealed demo emitter funnel
	_ = outbox.DemoCellTxManager()  // sealed demo tx-manager funnel
	_ = outbox.NewDirectCellEmitter // intended-L4 direct emitter ctor (function value)
	_ = outbox.ResolveEmitter       // durable-vs-direct resolver (function value)
	_ = eventbus.New                // sole sanctioned in-process publisher (function value)
)
