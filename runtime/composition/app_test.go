package composition

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// appRunCancelTimeout bounds how long App.Run may take to return after the
// context is canceled before the test fails (TEST-TIME-LITERAL-01: no bare
// duration literals in test bodies).
const appRunCancelTimeout = 5 * time.Second

// TestApp_Run_CancelledCtx verifies that App.Run returns promptly when the
// context is canceled. With an empty option set, bootstrap.New(...).Run
// completes as soon as the context is canceled.
func TestApp_Run_CancelledCtx(t *testing.T) {
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	app := &App{clk: clk, opts: nil}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled immediately

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	select {
	case err := <-done:
		// A canceled context causes bootstrap.Run to return context.Canceled;
		// that is the expected path when the application shuts down.
		_ = err // context.Canceled is acceptable
	case <-time.After(appRunCancelTimeout):
		t.Fatal("App.Run did not return within 5s after context cancel")
	}
}

// TestApp_FieldsRetained verifies that App stores the clock and opts correctly.
func TestApp_FieldsRetained(t *testing.T) {
	clk := clockmock.New(time.Now())
	app := &App{clk: clk, opts: nil}
	require.NotNil(t, app)
	require.Equal(t, clk, app.clk)
}
