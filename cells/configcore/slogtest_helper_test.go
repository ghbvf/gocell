package configcore

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/require"
)

// captureHandler is a test-only slog.Handler that records every Record for
// assertion. Mirrors the pattern in Go stdlib src/log/slog/logger_test.go.
// ref: https://github.com/golang/go/blob/master/src/log/slog/logger_test.go
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// findAttr returns the first slog.Attr with the given key from the record's
// top-level attributes.
func findAttr(r slog.Record, key string) (slog.Attr, bool) {
	var found slog.Attr
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found = a
			ok = true
			return false
		}
		return true
	})
	return found, ok
}

// TestConfigCore_InitDemoMode_EmitsL2DegradationWarn locks the cell_init.go
// L2 degradation warn log, preventing silent deletion (Round-2 T-R1/T-R3
// mutation-survivor fix). Verifies the warn carries the expected structured
// fields including the new durability_mode field added in Round-2 Fix 7.
func TestConfigCore_InitDemoMode_EmitsL2DegradationWarn(t *testing.T) {
	cap := &captureHandler{}
	logger := slog.New(cap)

	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithPublisher(eventbus.New()),
		WithLogger(logger),
	)
	require.NoError(t, c.Init(context.Background(),
		cell.Dependencies{DurabilityMode: cell.DurabilityDemo}))

	var warn *slog.Record
	for i := range cap.records {
		if cap.records[i].Level == slog.LevelWarn &&
			strings.Contains(cap.records[i].Message, "running without outboxWriter+txRunner") {
			warn = &cap.records[i]
			break
		}
	}
	require.NotNil(t, warn, "L2 degradation warn must be emitted in demo mode")

	cellAttr, ok := findAttr(*warn, "cell")
	require.True(t, ok, "L2 warn must carry 'cell' attribute")
	require.Equal(t, "configcore", cellAttr.Value.String())

	clAttr, ok := findAttr(*warn, "consistency_level")
	require.True(t, ok, "L2 warn must carry 'consistency_level' attribute")
	require.Equal(t, int64(2), clAttr.Value.Int64(), "L2 = 2")

	dmAttr, ok := findAttr(*warn, "durability_mode")
	require.True(t, ok, "L2 warn must carry 'durability_mode' attribute (Fix 7)")
	require.Equal(t, "demo", dmAttr.Value.String())
}
