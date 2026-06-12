//go:build archtest_fixture

// Package redqualified is a RED fixture for PROD-MAIN-WIRING-NOOP-REJECT-01: a
// synthetic composition-root package that directly constructs each of the five
// forbidden raw-noop sinks in qualified form (outbox.X). It proves the detector
// fires on every forbidden symbol, including the function-value form
// (outbox.NewDirectEmitter referenced without a call). The non-forbidden
// references (outbox.Writer / outbox.Publisher interface types) must NOT be
// flagged. Expect five diagnostics.
package redqualified

import "github.com/ghbvf/gocell/kernel/outbox"

var (
	_ outbox.Writer    = outbox.NoopWriter{}        // raw noop writer
	_                  = outbox.NewNoopEmitter()    // raw noop emitter (call)
	_                  = outbox.NewDirectEmitter    // direct emitter ctor (function value — bypasses ResolveEmitter)
	_ outbox.Publisher = &outbox.DiscardPublisher{} // raw noop publisher
	_                  = outbox.DemoTxRunner{}      // raw noop tx runner
)
