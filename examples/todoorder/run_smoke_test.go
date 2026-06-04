package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// smokeBootWindow bounds how long the in-process todoorder bootstrap runs before
// the test context deadline triggers graceful shutdown. Boot itself is
// millisecond-scale (all in-memory: NoopWriter / demoTxRunner / Mem projection
// stores / InMemClaimer / loopback listeners); the window only has to exceed
// boot time by a wide margin so the app reaches phase9 (await-shutdown) and
// returns via the deadline rather than being canceled mid-boot. Extracted per
// TEST-TIME-LITERAL-01.
const smokeBootWindow = 3 * time.Second

// smokeShutdownGrace is the extra slack on top of smokeBootWindow before the
// test declares app.Run wedged (the select-timeout is a hang backstop, not a
// fixed sleep). Generous on purpose: it bounds only the rare hang case (the
// happy path returns ~smokeBootWindow after the deadline fires), so a too-tight
// grace would risk flaking on a legitimately slow phase10 drain. Extracted per
// TEST-TIME-LITERAL-01.
const smokeShutdownGrace = 10 * time.Second

// smokeOperatorUsername / smokeOperatorPassword are test-only ephemeral operator
// credentials (set via t.Setenv, never persisted) used to exercise the
// AdminListener + projection-rebuild-endpoint branch. Password is ≥ 8 bytes per
// auth.NewAuthOperator. Extracted as consts to mirror testServiceKey (main_test.go).
const (
	smokeOperatorUsername = "todoorder-operator"
	smokeOperatorPassword = "operator-pass-1234"
)

// TestTodoorderBootstrapBootsThroughPhase6 is the startup smoke for the
// todoorder example (#1497). It boots the real run.go wiring
// (buildTodoorderBootstrap) on ephemeral loopback ports and asserts the app
// boots past phase6 — the projection coordinator stage that PR #1483's missing
// bootstrap.WithConsumerBase crashed at startup (CI was green because no test
// ever booted the example).
//
// Why a real boot (not just bootstrap.New): bootstrap.New only applies options;
// the projection↔ConsumerBase invariant is enforced in phase6 of Run, which
// runs before phase7 binds listeners. A regression (dropping WithConsumerBase)
// therefore makes Run return the phase6 error quickly — a non-context error —
// which this test fails on.
//
// Why ephemeral ports + deadline classification (not a /healthz probe):
// *bootstrap.Bootstrap exposes no bound-address accessor, so an ephemeral port
// cannot be probed; and the caller ctx gates phase3 / phase5 / lifecycle-start
// (bootstrap.go), so canceling too early could abort before phase6 and mask a
// regression. A generous deadline lets the app boot fully (ms) and sit at phase9
// until the window elapses, then return nil / a context error on graceful
// shutdown. Ephemeral loopback ports keep the smoke collision-free in CI.
//
// t.Setenv forbids t.Parallel; the subtests run sequentially.
func TestTodoorderBootstrapBootsThroughPhase6(t *testing.T) {
	cases := []struct {
		name          string
		operatorCreds bool // true exercises the AdminListener + projection-rebuild-endpoint branch
	}{
		{name: "operator_disabled_default_go_run_path"},
		{name: "operator_enabled_admin_listener_branch", operatorCreds: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reuse the env-assembly helpers from main_test.go (same package).
			setJWTKeyEnv(t)
			t.Setenv(jwtIssuerEnv, "todoorder-local")
			t.Setenv(jwtAudienceEnv, "gocell")
			t.Setenv(todoorderServiceSecretEnv, testServiceKey)
			if tc.operatorCreds {
				t.Setenv(operatorAdminUsernameEnv, smokeOperatorUsername)
				t.Setenv(operatorAdminPasswordEnv, smokeOperatorPassword)
			}

			// Ephemeral loopback for every listener so the smoke never collides
			// with a running demo or a sibling example smoke on the same runner.
			addrs := listenerAddrs{
				primary:  "127.0.0.1:0",
				internal: "127.0.0.1:0",
				health:   "127.0.0.1:0",
				admin:    "127.0.0.1:0",
			}

			app, err := buildTodoorderBootstrap("todoorder", []string{"ordercell"}, addrs)
			require.NoError(t, err, "buildTodoorderBootstrap must assemble without error")
			require.NotNil(t, app)

			ctx, cancel := context.WithTimeout(context.Background(), smokeBootWindow)
			defer cancel()

			runErrCh := make(chan error, 1)
			go func() { runErrCh <- app.Run(ctx) }()

			select {
			case runErr := <-runErrCh:
				// On the success path the app boots past phase6, sits at phase9,
				// and returns when the deadline fires — graceful shutdown yields nil
				// or a context error (the corebundlestarter smoke documents the same
				// nil/context-error contract). Any OTHER (non-context) error is a
				// real failure to surface: a startup-phase fail-fast (e.g. a
				// regression dropping WithConsumerBase crashes phase6 before any
				// listener binds — returned well before the deadline) or a teardown
				// error during shutdown. Both deserve a red test.
				if runErr != nil &&
					!errors.Is(runErr, context.DeadlineExceeded) &&
					!errors.Is(runErr, context.Canceled) {
					t.Fatalf("todoorder app.Run returned a non-context error "+
						"(boot-phase fail-fast or shutdown error): %v", runErr)
				}
			case <-time.After(smokeBootWindow + smokeShutdownGrace):
				t.Fatal("app.Run did not return within the boot window + grace after the deadline")
			}
		})
	}
}
