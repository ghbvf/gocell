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
// auto-discovers this package via the cells/{X}/{X}test$ naming pattern.
//
// # Relationship to production wiring
//
// Production wiring for configcore lives in cells/configcore/cell.go (Init,
// AfterStart, etc.) and is assembled in cmd/corebundle. This package replicates
// the same wiring decisions in a test-friendly way:
//   - Real service implementations (configwrite.NewService, configsubscribe.NewService).
//   - In-memory repository (cells/configcore/internal/mem.NewConfigRepository).
//   - kernel/cell.DemoCellTxManager() as a pass-through TxRunner.
//   - kernel/outbox/outboxtest.NewRecorder() as the event emitter.
//
// # Usage
//
//	svc, rec := configcoretest.BuildWriteService(t)
//	entry, err := svc.Create(ctx, configwrite.CreateInput{Key: "k", Value: "v"})
//	require.NoError(t, err)
//	require.Len(t, rec.Entries(), 1)
package configcoretest
