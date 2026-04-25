package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R2-03 + R2-06: shutdownCtxFor must:
//   - inherit the global shutdownTimeout when shutGrace == 0 (no override).
//   - bound the per-listener drain by min(parent.Deadline(), shutGrace) when
//     shutGrace > 0; in particular, shutGrace > parent.Deadline() must NOT
//     extend the drain past the global shutdownTimeout.

func TestShutdownCtxFor_ShutGraceZero_InheritsParentDeadline(t *testing.T) {
	t.Parallel()
	parentDeadline := time.Now().Add(2 * time.Second)
	parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	defer cancelParent()

	ctx, cancel := shutdownCtxFor(parent, 0)
	defer cancel()

	got, ok := ctx.Deadline()
	require.True(t, ok, "shutGrace==0 must return a ctx that inherits the parent deadline")
	assert.WithinDuration(t, parentDeadline, got, 50*time.Millisecond,
		"shutGrace==0 must yield exactly the parent deadline (no per-listener override)")
}

func TestShutdownCtxFor_ShutGraceZero_NoParentDeadline_NoCtxDeadline(t *testing.T) {
	t.Parallel()
	parent := context.Background()

	ctx, cancel := shutdownCtxFor(parent, 0)
	defer cancel()

	_, ok := ctx.Deadline()
	assert.False(t, ok, "shutGrace==0 with deadline-less parent must produce a deadline-less ctx")
}

func TestShutdownCtxFor_ShutGraceLargerThanParent_BoundedByParent(t *testing.T) {
	t.Parallel()
	// R2-03 root cause regression: parent has 2s budget, shutGrace asks for 10s.
	// Effective deadline must respect the parent's 2s budget — never extend
	// past the global shutdownTimeout. Pre-fix used context.Background() which
	// allowed the full 10s.
	parentDeadline := time.Now().Add(2 * time.Second)
	parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	defer cancelParent()

	ctx, cancel := shutdownCtxFor(parent, 10*time.Second)
	defer cancel()

	got, ok := ctx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, parentDeadline, got, 100*time.Millisecond,
		"shutGrace > parent budget must be capped to the parent deadline (R2-03)")
}

func TestShutdownCtxFor_ShutGraceSmallerThanParent_BoundedByGrace(t *testing.T) {
	t.Parallel()
	// shutGrace 200ms vs parent 5s → effective ~200ms (per-listener tighter
	// budget honoured).
	parentDeadline := time.Now().Add(5 * time.Second)
	parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	defer cancelParent()

	now := time.Now()
	ctx, cancel := shutdownCtxFor(parent, 200*time.Millisecond)
	defer cancel()

	got, ok := ctx.Deadline()
	require.True(t, ok)
	expected := now.Add(200 * time.Millisecond)
	assert.WithinDuration(t, expected, got, 50*time.Millisecond,
		"shutGrace < parent budget must produce ~now+shutGrace deadline")
}

func TestShutdownCtxFor_NoParentDeadline_GraceUsed(t *testing.T) {
	t.Parallel()
	parent := context.Background()

	now := time.Now()
	ctx, cancel := shutdownCtxFor(parent, 500*time.Millisecond)
	defer cancel()

	got, ok := ctx.Deadline()
	require.True(t, ok, "shutGrace > 0 with deadline-less parent must produce a deadline at now+shutGrace")
	assert.WithinDuration(t, now.Add(500*time.Millisecond), got, 50*time.Millisecond)
}
