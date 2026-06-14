package reconcile_test

// Adversarial regression guard for the PermanentError seal, run from an EXTERNAL
// package. package reconcile_test can reach the unexported *permanentError only
// via reflect — exactly the forgery model under threat. All foreign-hook and
// reflect-forgery vectors MUST classify NOT-permanent; only a genuinely-
// constructed marker (and errors that structurally wrap one) may be permanent.
// See reconcile.IsPermanent's godoc for why the non-nil err field is the
// construction proof.
//
// This is the non-vacuous reverse self-check for the seal (AI-robust): it FAILS
// against errors.As (vector as-returns-true), against errors.As && pe!=nil
// (as-reflect-set + unwrap-forged), and against a plain type-walk with no
// construction proof (unwrap-forged). Only the construction-proof walk passes.

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/reconcile"
)

// permanentErrorStructType recovers the unexported permanentError STRUCT type
// from the public constructor's return value (*permanentError → Elem()).
func permanentErrorStructType() reflect.Type {
	return reflect.TypeOf(reconcile.PermanentError(errors.New("seed"))).Elem()
}

// asReturnsTrue: As(any) bool returns true without setting target.
type asReturnsTrue struct{}

func (asReturnsTrue) Error() string { return "as-returns-true" }
func (asReturnsTrue) As(any) bool   { return true }

// asReflectSetsForged: As uses reflect to set the unexported target to a forged
// (zero-value, nil-cause) *permanentError.
type asReflectSetsForged struct{}

func (asReflectSetsForged) Error() string { return "as-reflect-set" }
func (asReflectSetsForged) As(target any) bool {
	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Pointer {
		return false
	}
	slot := rv.Elem() // *permanentError, settable
	if slot.Kind() != reflect.Pointer || !slot.CanSet() {
		return false
	}
	slot.Set(reflect.New(slot.Type().Elem())) // &permanentError{} (err == nil)
	return true
}

// unwrapForged: Unwrap returns a reflect-forged (nil-cause) *permanentError —
// defeats any chain-walk that matches type without a construction proof.
type unwrapForged struct{}

func (unwrapForged) Error() string { return "unwrap-forged" }
func (unwrapForged) Unwrap() error {
	return reflect.New(permanentErrorStructType()).Interface().(error) // &permanentError{} (err == nil)
}

func TestIsPermanent_ExternalForgeryMatrix(t *testing.T) {
	t.Parallel()
	genuine := reconcile.PermanentError(errors.New("revoked"))

	permanent := map[string]error{
		"genuine":            genuine,
		"wrapped-genuine-%w": fmt.Errorf("ctx: %w", genuine),
		"join-with-genuine":  errors.Join(errors.New("transient"), genuine),
	}
	for name, err := range permanent {
		if !reconcile.IsPermanent(err) {
			t.Errorf("IsPermanent(%s) = false, want true — a genuine marker is structurally present", name)
		}
	}

	notPermanent := map[string]error{
		"transient":              errors.New("plain"),
		"join-all-transient":     errors.Join(errors.New("a"), errors.New("b")),
		"as-returns-true":        asReturnsTrue{},
		"as-reflect-sets-forged": asReflectSetsForged{},
		"unwrap-forged":          unwrapForged{},
	}
	for name, err := range notPermanent {
		if reconcile.IsPermanent(err) {
			t.Errorf("IsPermanent(%s) = true, want false — foreign hooks and reflect-forged markers must not classify permanent", name)
		}
	}
}
