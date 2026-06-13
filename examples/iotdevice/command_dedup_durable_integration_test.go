//go:build integration

package main

// command_dedup_durable_integration_test.go — the durable, real-Postgres sibling
// of command_dedup_e2e_test.go (#1778).
//
// The demo E2E (TestCommandRelay_DedupOnEventRedelivery) drives the reactive
// producer→outbox→relay→Claimer loop over an in-memory outboxtest.FakeStore,
// which ignores the transaction context and fakes the poll/lease. That leaves
// three properties of the DURABLE production wiring unverified end-to-end:
// transaction atomicity (a rolled-back emit must leave NO row), real PG relay
// polling + Claimer dedup, and real PG MarkDead fail-closed. This test anchors
// all three against a testcontainers Postgres by exercising the SAME production
// assembly the binary uses — buildCommandRelaySubsystem(clk, eb, reg, pool) with
// a PG-backed pool — rather than re-wiring a replica.
//
// Both this file and command_dedup_e2e_test.go are package main, so under
// `-tags=integration` they compile together and this file reuses the demo's
// dedupEnqueueHandler / sourceEvent helpers verbatim (zero duplication).
//
// Single-pod note: buildCommandRelaySubsystem's durable branch refuses an
// in-memory command Claimer unless GOCELL_IOTDEVICE_DURABLE_SINGLE_POD
// acknowledges single-pod deployment — the iotdevice durable form IS single-pod
// (in-memory Claimer; a PG outbox coordinates row leases across pods, but the
// command_id Claimer does not). Cross-pod command dedup is a different
// (replaydeps / Redis) wiring and is out of scope here.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	devicebootstrap "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicebootstrap"
	enqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/runtime/command"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/tests/testutil"
)

// durableTestTenant is a canonical tenant UUID. The durable path is driven
// tenant-scoped (CLAUDE.md MDM-default: don't assume an implicit single tenant
// via context.Background()), so command dedup keys are tenant-scoped and the
// test additionally witnesses tenant propagation through the durable PG path.
const durableTestTenant = "11111111-1111-1111-1111-111111111111"

// TestCommandRelay_Durable_PG verifies the durable command-relay subsystem
// against real Postgres. One PG testcontainer is shared across the three
// sub-tests; each TRUNCATEs outbox_entries and builds a fresh subsystem +
// relay, and startDurableRelay stops the relay (waiting for its loop to exit)
// before the next sub-test runs so no lingering poller consumes the next
// sub-test's rows. Sub-tests are sequential (no t.Parallel — they share the
// pool and the single-pod env).
func TestCommandRelay_Durable_PG(t *testing.T) {
	testutil.RequireDocker(t)
	t.Setenv(envDurableSinglePod, "true")

	clk := clock.Real()
	pool, terminate := setupDurableOutboxPG(t)
	t.Cleanup(terminate)

	// ① Core dedup happy-path on real PG, tenant-scoped.
	t.Run("DedupOnEventRedelivery", func(t *testing.T) {
		truncateOutbox(t, pool)
		reg := command.NewRegistry()
		h := &dedupEnqueueHandler{}
		require.NoError(t, enqueue.Register(reg, h))

		crs, err := buildCommandRelaySubsystem(clk, &kout.DiscardPublisher{}, reg, pool)
		require.NoError(t, err)
		svc, err := devicebootstrap.NewService(clk,
			devicebootstrap.WithEmitter(crs.bootstrapEmitter),
			devicebootstrap.WithTxManager(crs.bootstrapTxManager))
		require.NoError(t, err)

		tenantCtx := ctxkeys.WithTenantID(context.Background(), durableTestTenant)
		src := sourceEvent(t, "d1", "evt-durable-redelivered")
		// At-least-once redelivery: handle the SAME source event twice → two
		// command entries carrying the SAME tenant-scoped command_id (distinct
		// store ids), written into PG outbox inside a real device-bootstrap tx.
		require.Equal(t, kout.DispositionAck, svc.HandleDeviceRegistered(tenantCtx, src).Disposition)
		require.Equal(t, kout.DispositionAck, svc.HandleDeviceRegistered(tenantCtx, src).Disposition)

		n, err := outboxCount(context.Background(), pool, "")
		require.NoError(t, err)
		require.Equal(t, 2, n, "two command entries must be written to PG outbox")
		require.Equal(t, []string{durableTestTenant, durableTestTenant}, outboxTenants(t, pool),
			"tenant must propagate through the durable PG path into the entry principal")

		startDurableRelay(t, crs.relay)
		require.Eventually(t, func() bool {
			pub, perr := outboxCount(context.Background(), pool, "published")
			return perr == nil && pub == 2
		}, 60*time.Second, 200*time.Millisecond,
			"both command entries must settle to published (one dispatched, one deduped)")

		require.Equal(t, 1, h.calls,
			"enqueue handler must run exactly once across the redelivered command (PG Claimer dedup)")
	})

	// ② Transaction atomicity — the property the demo FakeStore cannot witness.
	t.Run("TxAtomicity_RollbackLeavesNoRow", func(t *testing.T) {
		truncateOutbox(t, pool)
		reg := command.NewRegistry()
		crs, err := buildCommandRelaySubsystem(clk, &kout.DiscardPublisher{}, reg, pool)
		require.NoError(t, err)

		ctx := context.Background()
		req := enqueue.Request{DeviceID: "d2", CommandType: "bootstrap", Payload: "{}"}
		errBoom := errors.New("durable tx rollback boom")

		// Rollback: the PG outbox writer writes inside the device-bootstrap tx, so
		// a tx that emits then fails must leave NO command entry.
		rbErr := crs.bootstrapTxManager.RunInTx(ctx, func(txCtx context.Context) error {
			if e := command.EmitAsync(txCtx, clk, crs.bootstrapEmitter,
				enqueue.DispatchID, "d2", "evt-rollback", req); e != nil {
				return e
			}
			return errBoom
		})
		require.ErrorIs(t, rbErr, errBoom)
		n, err := outboxCount(ctx, pool, "")
		require.NoError(t, err)
		require.Equal(t, 0, n, "rolled-back tx must leave no command entry in PG outbox")

		// Commit: the same emit with a tx that succeeds → exactly one entry persists.
		require.NoError(t, crs.bootstrapTxManager.RunInTx(ctx, func(txCtx context.Context) error {
			return command.EmitAsync(txCtx, clk, crs.bootstrapEmitter,
				enqueue.DispatchID, "d2", "evt-commit", req)
		}))
		n, err = outboxCount(ctx, pool, "")
		require.NoError(t, err)
		require.Equal(t, 1, n, "committed tx must persist exactly one command entry")
	})

	// ③ Fail-closed dead-letter on missing identity, settled by real PG MarkDead.
	t.Run("FailClosedOnMissingIdentity_DeadLetter", func(t *testing.T) {
		truncateOutbox(t, pool)
		reg := command.NewRegistry()
		h := &dedupEnqueueHandler{}
		require.NoError(t, enqueue.Register(reg, h))

		crs, err := buildCommandRelaySubsystem(clk, &kout.DiscardPublisher{}, reg, pool)
		require.NoError(t, err)

		ctx := context.Background()
		// Identity-less command entry: schema-valid payload but no AggregateID and
		// no command_id metadata → ClaimKeyFromEntry returns ok=false. EmitAsync
		// always stamps a command_id, so seed via the raw PG writer instead.
		payload, err := json.Marshal(enqueue.Request{DeviceID: "d1", CommandType: "reboot", Payload: "now"})
		require.NoError(t, err)
		bad, err := kout.NewEntry(clk, ctx, string(enqueue.DispatchID), payload)
		require.NoError(t, err)
		writer := adapterpg.NewOutboxWriter(clk)
		require.NoError(t, crs.bootstrapTxManager.RunInTx(ctx, func(txCtx context.Context) error {
			return writer.Write(txCtx, bad)
		}))

		startDurableRelay(t, crs.relay)
		require.Eventually(t, func() bool {
			dead, derr := outboxCount(context.Background(), pool, "dead")
			return derr == nil && dead == 1
		}, 30*time.Second, 200*time.Millisecond,
			"identity-less command entry must be fail-closed dead-lettered on PG")
		require.Equal(t, 0, h.calls, "handler must not run on an identity-less entry")
	})
}

// setupDurableOutboxPG starts ONE PG testcontainer and applies the platform
// migrations once for the whole test. terminate closes the pool and stops the
// container. Mirrors setupSharedDeviceRepoPG in the sibling devicecell PG
// conformance test — a local per-file helper, not a cross-package harness.
func setupDurableOutboxPG(t *testing.T) (*adapterpg.Pool, func()) {
	t.Helper()
	// Required by archtest TestTestcontainerHelpersRequireDockerBeforeRun: the
	// function that calls tcpostgres.Run must also call RequireDocker (idempotent
	// — the top-level test already invoked it).
	testutil.RequireDocker(t)
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, durableMigrationsFS(t), migration.PlatformNamespace)
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	terminate := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: terminate postgres container: %v", err)
		}
	}
	return pool, terminate
}

// durableMigrationsFS returns the shared adapters/postgres migration FS.
// Mirrors the sibling devicecell PG test helper — Go _test.go files cannot be
// imported across packages, so the accessor is duplicated.
func durableMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}

// startDurableRelay starts the relay and, at sub-test end, cancels it and waits
// for its run loop to exit. Waiting is correctness (not just cleanliness): the
// pool is shared across sub-tests, so a lingering poller would consume the next
// sub-test's outbox rows.
func startDurableRelay(t *testing.T, relay *outboxruntime.Relay) {
	t.Helper()
	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = relay.Start(relayCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// truncateOutbox clears outbox_entries so each sub-test starts from empty.
func truncateOutbox(t *testing.T, pool *adapterpg.Pool) {
	t.Helper()
	_, err := pool.DB().Exec(context.Background(), "TRUNCATE TABLE outbox_entries")
	require.NoError(t, err, "truncate outbox_entries")
}

// outboxCount returns the number of outbox_entries rows, optionally filtered by
// status (status == "" counts all). It returns an error rather than failing the
// test so it is safe to call inside require.Eventually condition callbacks
// (which run off the test goroutine).
func outboxCount(ctx context.Context, pool *adapterpg.Pool, status string) (int, error) {
	var (
		n   int
		err error
	)
	if status == "" {
		err = pool.DB().QueryRow(ctx, "SELECT count(*) FROM outbox_entries").Scan(&n)
	} else {
		err = pool.DB().QueryRow(ctx, "SELECT count(*) FROM outbox_entries WHERE status = $1", status).Scan(&n)
	}
	return n, err
}

// outboxTenants returns the principal tenant id of every outbox_entries row
// (empty string when the principal carries no tenant). Used to assert tenant
// propagation through the durable PG path.
func outboxTenants(t *testing.T, pool *adapterpg.Pool) []string {
	t.Helper()
	rows, err := pool.DB().Query(context.Background(), "SELECT principal->>'tenantId' FROM outbox_entries")
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var tenant *string
		require.NoError(t, rows.Scan(&tenant))
		if tenant == nil {
			out = append(out, "")
		} else {
			out = append(out, *tenant)
		}
	}
	require.NoError(t, rows.Err())
	return out
}
