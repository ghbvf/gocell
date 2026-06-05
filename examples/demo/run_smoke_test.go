package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// smokeBootWindow bounds how long the in-process demo bootstrap runs before the
// test context deadline triggers graceful shutdown. Boot is millisecond-scale (a
// single in-memory L1 cell + two loopback listeners); the window only needs to
// exceed boot time by a wide margin so the app reaches the await-shutdown phase
// and returns via the deadline rather than being canceled mid-boot. Extracted
// per TEST-TIME-LITERAL-01.
const smokeBootWindow = 3 * time.Second

// smokeShutdownGrace is the extra slack on top of smokeBootWindow before the
// test declares app.Run wedged (a hang backstop, not a fixed sleep). Generous on
// purpose: the happy path returns ~smokeBootWindow after the deadline fires, so
// this only bounds the rare hang case. Extracted per TEST-TIME-LITERAL-01.
const smokeShutdownGrace = 10 * time.Second

// TestDemoBootstrapBoots is the startup smoke for the demo example. It boots the
// real run.go wiring (buildDemoBootstrap) on ephemeral loopback ports and
// asserts the app boots and shuts down cleanly. A regression in the cell/slice
// wiring — a missing slice metadata, a malformed +slice:route marker, an
// unmountable handler — surfaces as a non-context error returned well before the
// deadline (CI would otherwise be green because no test ever booted the example;
// see PR #1497 / the todoorder smoke for the same rationale).
//
// Why a real boot (not just bootstrap.New): bootstrap.New only applies options;
// route mounting + FinalizeAuth run inside Run, before the listeners bind. A
// regression therefore makes Run return a non-context error quickly, which this
// test fails on.
//
// Why ephemeral ports + deadline classification (not a /healthz probe):
// *bootstrap.Bootstrap exposes no bound-address accessor, so an ephemeral port
// cannot be probed; a generous deadline lets the app boot fully (ms) and sit at
// await-shutdown until the window elapses, then return nil / a context error on
// graceful shutdown. Ephemeral loopback ports keep the smoke collision-free.
func TestDemoBootstrapBoots(t *testing.T) {
	t.Parallel()

	// Ephemeral loopback for both listeners so the smoke never collides with a
	// running demo or a sibling example smoke on the same runner.
	addrs := listenerAddrs{primary: "127.0.0.1:0", health: "127.0.0.1:0"}

	app, err := buildDemoBootstrap("demo", []string{"democell"}, addrs)
	require.NoError(t, err, "buildDemoBootstrap must assemble without error")
	require.NotNil(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), smokeBootWindow)
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- app.Run(ctx) }()

	select {
	case runErr := <-runErrCh:
		// Success path: the app boots, sits at await-shutdown, and returns when
		// the deadline fires — graceful shutdown yields nil or a context error.
		// Any OTHER (non-context) error is a real boot-phase fail-fast or a
		// teardown error and deserves a red test.
		if runErr != nil &&
			!errors.Is(runErr, context.DeadlineExceeded) &&
			!errors.Is(runErr, context.Canceled) {
			t.Fatalf("demo app.Run returned a non-context error "+
				"(boot-phase fail-fast or shutdown error): %v", runErr)
		}
	case <-time.After(smokeBootWindow + smokeShutdownGrace):
		t.Fatal("app.Run did not return within the boot window + grace after the deadline")
	}
}
