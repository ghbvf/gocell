// Package configcoretest provides docker-free test helpers for the configcore
// cell. It is intended for use in producer-side and state-apply journey
// criterion tests that need an in-memory wiring of configwrite or
// configsubscribe services without standing up a real database or message
// broker.
//
// # Relation to production wiring
//
// Production wiring (cmd/corebundle, assembly composition roots) injects
// postgres-backed repositories and transactional outbox emitters. This package
// replaces those with:
//
//   - FakeConfigRepository — an in-memory map-backed ports.ConfigRepository
//   - kernel/cell.DemoCellTxManager — a pass-through TxRunner sealed as
//     persistence.CellTxManager for injection into configwrite.WithTxManager.
//     Note: DemoCellTxManager is a testutil/demo-only factory and is not the
//     persistence.WrapForCell composition root path used in production.
//   - kernel/outbox/outboxtest.Recorder — in-memory Emitter that captures
//     emitted entries for assertion
//
// # Import scope
//
// This package must not be imported by production (non-test) code. The rule is
// enforced by archtest CELLTEST-IMPORT-SCOPE-01: any non-_test.go file outside
// a test-infra path that imports cells/configcore/configcoretest will cause the
// archtest to fail.
package configcoretest
