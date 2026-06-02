//go:build archtest_fixture

// Package audittraceididfixture is the AUDIT-TRACE-ID-WRITE-CALLER-01
// negative fixture. It exercises the two value-write shapes the rule
// prohibits — a composite literal field write and a direct assignment —
// both from a non-appender, non-reconstruction context.
//
// Loaded only when the archtest_fixture build tag is set. Never imported
// from production code. The scanner pointed at this package must report
// at least two violations:
//  1. badCompositeLit — composite literal write of ledger.Entry.TraceID.
//  2. badAssignment   — direct assignment e.TraceID = value.
//
// A third function (addressTake) takes &e.TraceID for a Scan call; that
// shape is a blind-spot the rule cannot distinguish from a value write at
// the UnaryExpr level — documented as reconstruction scan-target blind
// spot #1 in the archtest godoc.
//
// DO NOT use this package in production code.
package audittraceididfixture

import "github.com/ghbvf/gocell/runtime/audit/ledger"

// badCompositeLit fabricates a ledger.Entry with an explicit TraceID value
// via a composite literal. This is violation shape #1: a keyed composite
// literal field write of ledger.Entry.TraceID from a file that is not in
// the injection or reconstruction allowlists.
func badCompositeLit() *ledger.Entry {
	return &ledger.Entry{
		EventID:   "fixture-event-id",
		EventType: "fixture.event.v1",
		ActorID:   "fixture-actor",
		TraceID:   "fabricated-trace-id", // VIOLATION: non-appender TraceID composite lit
	}
}

// badAssignment fabricates a TraceID value via a direct assignment statement.
// This is violation shape #2: an AssignStmt LHS write of ledger.Entry.TraceID
// from a file that is not in the injection allowlist. This shape simulates the
// scenario the F6 granularity tightening is designed to catch: code inside a
// postgres-reconstruction-like file that adds a value-assignment alongside
// legitimate scan-address takes.
func badAssignment() *ledger.Entry {
	e := &ledger.Entry{}
	e.TraceID = "fabricated-trace-id" // VIOLATION: direct assignment outside allowlist
	return e
}

// addressTake takes &e.TraceID and passes it to a Scan-like function.
// This is the blind-spot #1 shape: a UnaryExpr & applied to a SelectorExpr
// in a function-call argument position. The scanner checks AssignStmt LHS
// and CompositeLit Elts — neither matches this shape, so it is NOT flagged.
// This function exists to confirm the shape is present in the fixture but
// does not produce a violation.
func addressTake(scan func(...any)) *ledger.Entry {
	e := &ledger.Entry{}
	scan(&e.TraceID) // NOT a violation: UnaryExpr & scan address, not an assignment LHS
	return e
}
