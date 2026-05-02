package router

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
)

func TestNewForListener_RequiresClock(t *testing.T) {
	_, err := NewForListener(cell.PrimaryListener) // no WithRouterClock
	if err == nil {
		t.Fatal("expected error when clock is missing")
	}
	if !strings.Contains(err.Error(), "clock") {
		t.Fatalf("error should mention clock: %v", err)
	}
}
