// Package nilutil provides typed-nil aware nil checks for interface values
// used by wiring/option functions to fail-fast on nil before runtime panics.
//
// Background: a typed-nil interface (non-nil interface wrapping a nil
// pointer/map/slice/chan/func) passes a plain v == nil check because the
// interface value carries type information, but calling any method on the
// underlying pointer panics.
//
// Usage: call after a bare-nil interface check is desired short-circuit, or
// pass any interface value directly — IsNil handles both bare-nil and
// typed-nil with a single kind-gated reflect guard.
//
// ref: golang.org/src/reflect Value.IsNil — kind-gated guard.
package nilutil

import "reflect"

// IsNil reports whether v is bare-nil or a typed-nil interface wrapping a
// nil pointer/map/slice/chan/func/interface. For non-nilable kinds (int,
// string, struct, etc.) it returns false unconditionally.
func IsNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice,
		reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}
