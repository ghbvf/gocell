// Package auditcoretest provides docker-free testutil helpers for auditcore
// Cell wiring.
//
// Use this package in integration tests and journey conformance suites that
// need a fully-wired auditcore Cell without spinning up a database or broker.
// The default wiring uses MemStore + NoopEmitter + DemoCellTxManager + HMAC
// test key + clock.Real, matching the production cell contract at the
// subscription seam.
//
// # Relation to production wiring
//
// BuildAuditcoreChain constructs an AuditCore instance via the same public
// Option API used by production composition roots (cmd/corebundle). The only
// differences are the store (MemStore vs PG) and the emitter (NoopEmitter vs
// transactional DirectEmitter). All business logic paths—handler, ledger
// Append, HMAC chain—are exercised identically.
//
// # Import scope
//
// This package MUST NOT be imported by production code. It is test
// infrastructure; only *_test.go files and other test-infra packages may
// import it. The archtest rule CELLTEST-IMPORT-SCOPE-01
// (tools/archtest/celltest_import_scope_test.go) enforces this statically.
package auditcoretest
