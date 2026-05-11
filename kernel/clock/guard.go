package clock

import (
	"reflect"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// MustHaveClock panics when c is nil or when c is an interface value wrapping
// a nil pointer (typed-nil). Use at construction boundaries to fail fast on
// missing Clock wiring; the panic is wrapped with panicregister.Approved per
// PANIC-REGISTERED-01.
//
// ctx names the call site (e.g. "assembly.New") so the panic message identifies
// which constructor is missing wiring.
//
// ref: docs/architecture/202604270030-architectural-panic-whitelist.md §4
// We share a single helper across kernel/assembly,
// runtime/bootstrap, runtime/http/health, etc. so the panic message stays
// uniform and the gate's auditing has exactly one location.
func MustHaveClock(c Clock, ctx string) {
	const (
		nilMsg = "%s: clock.Clock is required (nil rejected); " +
			"pass clock.Real() at the composition root or clockmock.New(...) in tests"
		typedNilMsg = "%s: clock.Clock is required (typed-nil rejected); " +
			"pass clock.Real() at the composition root or clockmock.New(...) in tests"
	)
	if c == nil {
		panic(panicregister.Approved("clock-nil", errcode.Assertion(nilMsg, ctx)))
	}
	v := reflect.ValueOf(c)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice, reflect.Interface:
		if v.IsNil() {
			panic(panicregister.Approved("clock-typed-nil", errcode.Assertion(typedNilMsg, ctx)))
		}
	}
}

// MustHavePositiveInterval panics when d is non-positive. Mirrors stdlib
// time.NewTicker / time.Ticker.Reset semantics (both panic on d <= 0). ctx
// names the call site so callers can identify the misuse from the panic.
func MustHavePositiveInterval(d time.Duration, ctx string) {
	if d <= 0 {
		panic(panicregister.Approved("clock-non-positive-interval", errcode.Assertion("%s: non-positive interval (got %s)", ctx, d.String())))
	}
}
