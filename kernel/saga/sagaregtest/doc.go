// Package sagaregtest provides a reusable conformance suite for
// implementations of [saga.Registry]. Any implementation — in-memory
// (PR-03) or contractgen-emitted SagaRegistry_<Name> (PR-07) — calls
// RunConformance(t, factory, defs...) to verify the full Lookup contract.
//
// The suite is a plain .go file (not _test.go) so it can be imported by test
// packages in other layers without being stripped from the build graph.
// Pattern mirrors kernel/saga/sagajournaltest.
package sagaregtest
