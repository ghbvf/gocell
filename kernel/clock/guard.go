package clock

import (
	"reflect"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// MustHaveClock panics when c is nil or when c is an interface value wrapping
// a nil pointer (typed-nil). Use at construction boundaries to fail fast on
// missing Clock wiring; the Must prefix marks this as a programmer-error-only
// API exempt from PANIC-REGISTERED-01.
//
// ctx names the call site (e.g. "assembly.New") so the panic message identifies
// which constructor is missing wiring.
//
// ref: docs/architecture/202604270030-architectural-panic-whitelist.md §5
// — Must* prefix is the canonical "panic-on-misuse" twin of error-returning
// constructors. We share a single helper across kernel/assembly,
// runtime/bootstrap, runtime/http/health, etc. so the panic message stays
// uniform and the gate's exempt-list auditing has exactly one location.
func MustHaveClock(c Clock, ctx string) {
	if c == nil {
		panic(errcode.Assertion("%s: clock.Clock is required (nil rejected); pass clock.Real() at the composition root or clockmock.New(...) in tests", ctx))
	}
	v := reflect.ValueOf(c)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice, reflect.Interface:
		if v.IsNil() {
			panic(errcode.Assertion("%s: clock.Clock is required (typed-nil rejected); pass clock.Real() at the composition root or clockmock.New(...) in tests", ctx))
		}
	}
}

// MustHavePositiveInterval panics when d is non-positive. Mirrors stdlib
// time.NewTicker / time.Ticker.Reset semantics (both panic on d <= 0). ctx
// names the call site so callers can identify the misuse from the panic.
func MustHavePositiveInterval(d time.Duration, ctx string) {
	if d <= 0 {
		panic(errcode.Assertion("%s: non-positive interval (got %s)", ctx, d.String()))
	}
}
