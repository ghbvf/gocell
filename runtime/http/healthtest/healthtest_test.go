package healthtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCheckCtxRespected_PassesOnCooperativeProbe is a minimal smoke test
// for the exported helper that probe authors will use in their own unit
// tests. A cooperative probe must cause zero failures.
func TestCheckCtxRespected_PassesOnCooperativeProbe(t *testing.T) {
	cooperative := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	CheckCtxRespected(t, cooperative, 50*time.Millisecond)
}

// TestCheckCtxRespected_DetectsUncooperativeProbe exercises the failure
// path by running the helper against a deliberately-stuck probe, capturing
// the t.Errorf call via a testing.TB spy.
func TestCheckCtxRespected_DetectsUncooperativeProbe(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	stuck := func(_ context.Context) error {
		<-unblock
		return nil
	}
	spy := &tbSpy{TB: t}
	CheckCtxRespected(spy, stuck, 30*time.Millisecond)
	assert.True(t, spy.errored, "CheckCtxRespected must flag an uncooperative probe")
	assert.Contains(t, spy.lastMsg, "did not return within")
}

// tbSpy captures t.Errorf calls without failing the enclosing test.
type tbSpy struct {
	testing.TB
	errored bool
	lastMsg string
}

func (s *tbSpy) Errorf(format string, args ...any) {
	s.errored = true
	s.lastMsg = fmt.Sprintf(format, args...)
}
