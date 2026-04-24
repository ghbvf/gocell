package command

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Compile-time interface checks
// ---------------------------------------------------------------------------

type mockWriter struct{}

func (m *mockWriter) WriteCommand(_ context.Context, _ Entry) error { return nil }

var _ Writer = (*mockWriter)(nil)

type mockReader struct{}

func (m *mockReader) PendingCommands(_ context.Context, _ string) ([]Entry, error) {
	return nil, nil
}
func (m *mockReader) GetCommand(_ context.Context, _ string) (*Entry, error) {
	return nil, nil
}

var _ Reader = (*mockReader)(nil)

type mockStateAdvancer struct{}

func (m *mockStateAdvancer) AdvanceStatus(_ context.Context, _ string, _, _ Status, _ time.Time) error {
	return nil
}

var _ StateAdvancer = (*mockStateAdvancer)(nil)

func TestPorts_InterfaceCompile(t *testing.T) {
	t.Parallel()
	// If this compiles, the interface checks above passed.
	_ = &mockWriter{}
	_ = &mockReader{}
	_ = &mockStateAdvancer{}
}
