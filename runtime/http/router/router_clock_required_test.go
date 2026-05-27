package router

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
)

// TestNewForListener_NilClockPanics verifies that passing a typed-nil clock
// panics via clock.MustHaveClock. The clock.Clock parameter is now a mandatory
// positional argument enforced at the call site; typed-nil is the only
// programmer error that can slip through.
func TestNewForListener_NilClockPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when typed-nil clock is passed")
		}
	}()
	var clk clock.Clock // typed nil
	_, _ = NewForListener(clk, cell.PrimaryListener)
}
