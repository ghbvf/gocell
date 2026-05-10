package command_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestNewSweeper_NilScanner_FailFast pins the wiring-option fail-fast pattern
// (runtime-api.md §"强依赖 wiring option"). Construction-time validation is
// the AI-HARD form of this invariant: callers can no longer construct an
// invalid Sweeper via struct literal (fields are unexported in Wave 2 GREEN).
func TestNewSweeper_NilScanner_FailFast(t *testing.T) {
	t.Parallel()
	_, err := command.NewSweeper(nil, &mockAckQueue{}, clock.Real())
	if err == nil {
		t.Fatal("NewSweeper(nil scanner) must return error")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Code != errcode.ErrValidationFailed {
		t.Fatalf("expected ErrValidationFailed, got %T %v", err, err)
	}
}

func TestNewSweeper_NilQueue_FailFast(t *testing.T) {
	t.Parallel()
	_, err := command.NewSweeper(&mockScanner{}, nil, clock.Real())
	if err == nil {
		t.Fatal("NewSweeper(nil queue) must return error")
	}
}

func TestNewSweeper_NilClock_FailFast(t *testing.T) {
	t.Parallel()
	_, err := command.NewSweeper(&mockScanner{}, &mockAckQueue{}, nil)
	if err == nil {
		t.Fatal("NewSweeper(nil clk) must return error")
	}
}

func TestNewSweeper_AllRequired_OK(t *testing.T) {
	t.Parallel()
	s, err := command.NewSweeper(&mockScanner{}, &mockAckQueue{}, clock.Real(),
		command.WithSweeperInterval(time.Hour),
		command.WithSweeperFilter(command.ScanFilter{DeviceID: "dev-1"}),
		command.WithSweeperOnError(func(error) {}))
	if err != nil {
		t.Fatalf("NewSweeper err: %v", err)
	}
	if s == nil {
		t.Fatal("NewSweeper returned nil sweeper")
	}
}
