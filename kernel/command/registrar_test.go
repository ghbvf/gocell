package command_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/command"
)

// stubQueueRegistrar implements QueueRegistrar for compile-time verification.
type stubQueueRegistrar struct {
	queue command.Queue
}

func (s *stubQueueRegistrar) RegisterCommandQueue(q command.Queue) {
	s.queue = q
}

// Compile-time check.
var _ command.QueueRegistrar = (*stubQueueRegistrar)(nil)

func TestQueueRegistrar_InterfaceCompile(t *testing.T) {
	t.Parallel()
	// If this file compiles, the interface check above passed.
	_ = &stubQueueRegistrar{}
}
