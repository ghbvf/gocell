//go:build archtest_fixture

// Package redqualified is a RED fixture for PROD-MAIN-WIRING-NOOP-REJECT-01: a
// synthetic composition-root package that directly references each of the five
// forbidden raw-noop sinks in qualified form (outbox.X), across construction
// forms — composite literal, call, function value, and embedded field. It proves
// the detector fires on every forbidden symbol and every reference form. The
// non-forbidden references (outbox.Writer / outbox.Publisher interface types)
// must NOT be flagged. Expect six diagnostics.
package redqualified

import "github.com/ghbvf/gocell/kernel/outbox"

var (
	_ outbox.Writer    = outbox.NoopWriter{}        // raw noop writer
	_                  = outbox.NewNoopEmitter()    // raw noop emitter (call)
	_                  = outbox.NewDirectEmitter    // direct emitter ctor (function value — bypasses ResolveEmitter)
	_ outbox.Publisher = &outbox.DiscardPublisher{} // raw noop publisher
	_                  = outbox.DemoTxRunner{}      // raw noop tx runner
)

// embeddedNoop exercises the embedded-field form: the embedded type reference
// outbox.NoopWriter is an ast.SelectorExpr inside a type declaration, caught by
// the same SelectorExpr walk as a composite literal.
type embeddedNoop struct{ outbox.NoopWriter }
