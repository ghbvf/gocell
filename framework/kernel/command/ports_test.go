package command

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Compile-time interface checks
// ---------------------------------------------------------------------------

type mockWriter struct{}

func (m *mockWriter) WriteCommand(_ context.Context, _ Entry) error { return nil }

var _ Writer = (*mockWriter)(nil)

type mockActiveScanner struct{}

func (m *mockActiveScanner) ScanActive(_ context.Context, _ ScanFilter) ([]Entry, error) {
	return nil, nil
}

func (m *mockActiveScanner) GetCommand(_ context.Context, _ string) (*Entry, error) {
	return &Entry{}, nil
}

var _ ActiveScanner = (*mockActiveScanner)(nil)

func TestPorts_InterfaceCompile(t *testing.T) {
	t.Parallel()
	// If this compiles, the interface checks above passed.
	_ = &mockWriter{}
	_ = &mockActiveScanner{}
}
