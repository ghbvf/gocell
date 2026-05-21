// Package accesscoretest provides docker-free test helpers for the accesscore
// cell. It is intended for use in unit tests of slices and journeys that depend
// on accesscore ports without a real database.
//
// # Provided helpers
//
//   - FakeConfigGetter — implements ports.ConfigGetter; returns preset stubs or
//     errcode.ErrConfigNotFound by default.
//   - FakeUserRepo — implements ports.UserRepository; in-memory, concurrency-safe.
//   - FakeRoleRepo — implements ports.RoleRepository; in-memory, concurrency-safe.
//   - NewCredentialInvalidator — constructs a real *credentialinvalidate.Invalidator
//     backed by stub session/refresh stores; suitable for unit tests that need the
//     invalidator trifecta without a database.
//   - BuildConfigReceiveService — constructs a configreceive.Service wired with a
//     default FakeConfigGetter (all keys return ErrConfigNotFound).
//   - BuildIdentityManageService — constructs an identitymanage.Service wired with
//     FakeUserRepo + FakeRoleRepo + outboxtest.Recorder.
//
// # Relation to production wiring
//
// This package mirrors the production composition root but substitutes real
// adapters with in-memory fakes. The fake implementations satisfy the same
// interface contracts as the PG adapters; tests that pass here can be promoted
// to integration tests by swapping the fakes for real stores.
//
// # Import scope constraint
//
// This package MUST NOT be imported from production (non-_test.go) code.
// The constraint is enforced by archtest CELLTEST-IMPORT-SCOPE-01 in
// tools/archtest/celltest_import_scope_test.go.
package accesscoretest
