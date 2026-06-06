package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestWebhookdemoBootSmoke boots the real webhookdemo wiring on ephemeral
// loopback ports (the string-addr build path `go run` uses) and asserts it boots
// through every bootstrap phase and shuts down gracefully. This is the startup
// defense-in-depth every example carries: a regression that fails fast in a boot
// phase (e.g. an undeclared WebhookListener, or a missing webhook source store)
// returns a non-context error well before the deadline, turning this test red.
func TestWebhookdemoBootSmoke(t *testing.T) {
	t.Parallel()
	addrs := webhookdemoAddrs{
		webhook: listenerBinding{addr: "127.0.0.1:0"},
		health:  listenerBinding{addr: "127.0.0.1:0"},
	}
	app, err := buildWebhookdemoBootstrap("webhookdemo", []string{"hooks"}, addrs)
	require.NoError(t, err, "buildWebhookdemoBootstrap must assemble without error")
	require.NotNil(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- app.Run(ctx) }()

	select {
	case runErr := <-runErrCh:
		// Happy path: boots, sits at await-shutdown, returns when the deadline
		// fires → nil or a context error. Any other error is a boot-phase
		// fail-fast or a teardown error worth surfacing.
		if runErr != nil &&
			!errors.Is(runErr, context.DeadlineExceeded) &&
			!errors.Is(runErr, context.Canceled) {
			t.Fatalf("webhookdemo app.Run returned a non-context error "+
				"(boot-phase fail-fast or shutdown error): %v", runErr)
		}
	case <-time.After(testtime.EventuallyDefault + testtime.SelectShutdown):
		t.Fatal("app.Run did not return within the boot window + grace after the deadline")
	}
}

// TestDefaultWebhookdemoAddrs covers the default listener bindings the `go run`
// entrypoint (runWebhookdemo) feeds into buildWebhookdemoBootstrap.
func TestDefaultWebhookdemoAddrs(t *testing.T) {
	t.Parallel()
	addrs := defaultWebhookdemoAddrs()
	require.NotEmpty(t, addrs.webhook.addr)
	require.NotEmpty(t, addrs.health.addr)
	require.Nil(t, addrs.webhook.ln, "default bindings carry no pre-bound listener")
	require.Nil(t, addrs.health.ln)
}

// TestAssertModuleIDsMatch covers the assembly drift check (assembly.yaml cells ↔
// modules_gen.go): the matching case plus both fail-fast branches.
func TestAssertModuleIDsMatch(t *testing.T) {
	t.Parallel()
	mods := generatedCellModules() // [HooksCellModule{}]

	t.Run("match", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, assertModuleIDsMatch("webhookdemo", []string{"hooks"}, mods))
	})

	t.Run("length mismatch", func(t *testing.T) {
		t.Parallel()
		err := assertModuleIDsMatch("webhookdemo", []string{"hooks", "extra"}, mods)
		require.Error(t, err)
		require.Contains(t, err.Error(), "length mismatch")
	})

	t.Run("id mismatch", func(t *testing.T) {
		t.Parallel()
		err := assertModuleIDsMatch("webhookdemo", []string{"wrongcell"}, mods)
		require.Error(t, err)
		require.Contains(t, err.Error(), "drift")
	})
}
