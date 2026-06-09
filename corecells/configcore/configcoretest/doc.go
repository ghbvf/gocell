// Package configcoretest provides testutil builders and fakes for the configcore
// cell. It exposes constructors for the configwrite and configsubscribe slices
// without requiring Docker or real infrastructure, enabling fast, docker-free
// unit and journey-criterion tests.
//
// # Import scope
//
// This package must only be imported from test code: *_test.go files or other
// test-infrastructure packages (paths containing a "testutil" segment or a
// segment ending in "test"). Production binaries must never import it.
// Enforcement: archtest CELLTEST-IMPORT-SCOPE-01 (tools/archtest/celltest_import_scope_test.go)
// auto-discovers this package via the corecells/{X}/{X}test$ naming pattern.
//
// # Relationship to production wiring
//
// Production wiring for configcore lives in corecells/configcore/cell.go (Init,
// AfterStart, etc.) and is assembled in cmd/corebundle. This package replicates
// the same wiring decisions in a test-friendly way:
//   - Real service implementations (configwrite.NewService, configsubscribe.NewService).
//   - An in-memory repository (backed by the mem package — always zero-latency,
//     no external dependencies).
//   - kernel/outbox.DemoCellTxManager() as a pass-through TxRunner.
//   - kernel/outbox/outboxtest.NewRecorder() as the event emitter.
//
// # Relationship to internal/testutil
//
// corecells/configcore/internal/testutil provides low-level raw doubles (fake
// repositories, stub ports) intended for in-package unit tests within the
// configcore subtree. Import it only from test files inside corecells/configcore/.
//
// configcoretest (this package) provides higher-level Service builders for
// cross-package tests and Journey-criterion tests outside corecells/configcore. It
// wires full service graphs so callers interact with the same service API that
// production code uses — no knowledge of internal types or ports required.
//
// Rule of thumb: if your test file lives inside corecells/configcore/ and needs a
// raw double, use internal/testutil. If your test lives outside corecells/configcore/
// (e.g. tests/integration/, journeys/, or a sibling cell), use configcoretest.
//
// # Usage
//
//	svc, repo, rec := configcoretest.BuildWriteService(t)
//	entry, err := svc.Create(ctx, configwrite.CreateInput{Key: "k", Value: "v"})
//	require.NoError(t, err)
//	require.Len(t, rec.Entries(), 1)
//	snap, _ := repo.Snapshot(ctx) // direct repo handle for state assertions
//
// # Clock single-source contract
//
// BuildWriteService always constructs the repository internally using the
// configured clock (WithWriteClock, defaults to clock.Real()). There is no
// option to inject a pre-built repository: the service and repository must
// share one clock so that Create-stamped CreatedAt/UpdatedAt and
// Update-stamped UpdatedAt come from the same time source.
//
// # Debug logging
//
// By default builders discard all logs. To surface slog output during
// debugging, inject a real handler:
//
//	svc, repo, rec := configcoretest.BuildWriteService(t,
//	    configcoretest.WithWriteLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))),
//	)
//	_ = repo
//	_ = rec
package configcoretest
