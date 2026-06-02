//go:build archtest_fixture

// Package audittraceidfixture is the AUDIT-TRACE-ID-WRITE-CALLER-01 negative
// fixture. It exercises every value-write shape the rule prohibits — keyed and
// positional composite literals plus direct assignment — for BOTH guarded
// fields (TraceID and CorrelationID), all from a non-appender,
// non-reconstruction context.
//
// Loaded only when the archtest_fixture build tag is set. Never imported from
// production code. The scanner pointed at this package must report exactly six
// violations:
//  1. badCompositeLit          — keyed composite literal writes of TraceID AND
//     CorrelationID (two).
//  2. badPositionalLit         — positional (unkeyed) composite literal; the
//     CorrelationID and TraceID positions are both guarded (two). This is the
//     F3 shape: a keyed-only scanner would miss it.
//  3. badAssignment            — direct assignment e.TraceID = value (one).
//  4. badAssignmentCorrelation — direct assignment e.CorrelationID = value (one).
//
// Totals: 4 composite (2 keyed + 2 positional) + 2 assignment = 6.
//
// A further function (addressTake) takes &e.TraceID for a Scan call; that shape
// is a blind-spot the rule cannot distinguish from a value write at the
// UnaryExpr level — documented as reconstruction scan-target blind spot #1 in
// the archtest godoc. It contributes zero violations.
//
// DO NOT use this package in production code.
package audittraceidfixture

import (
	"time"

	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// badCompositeLit fabricates a ledger.Entry with explicit TraceID AND
// CorrelationID values via a keyed composite literal. This is violation shape #1
// for both guarded fields, from a file that is not in the injection or
// reconstruction allowlists.
func badCompositeLit() *ledger.Entry {
	return &ledger.Entry{
		EventID:       "fixture-event-id",
		EventType:     "fixture.event.v1",
		ActorID:       "fixture-actor",
		TraceID:       "fabricated-trace-id",       // VIOLATION: non-appender TraceID composite lit
		CorrelationID: "fabricated-correlation-id", // VIOLATION: non-appender CorrelationID composite lit
	}
}

// badPositionalLit fabricates a ledger.Entry via a POSITIONAL (unkeyed)
// composite literal — violation shape #2 (F3). A keyed-only scanner cannot see
// the guarded fields here because there are no KeyValueExpr nodes; detection
// relies on mapping each element index to the struct field at that index.
//
// The value list MUST mirror ledger.Entry's field declaration order. The
// guarded positions are CorrelationID (index 8) and TraceID (index 9). If the
// struct layout changes this literal stops compiling — that is intentional: the
// positional-detection contract is tied to the field order.
func badPositionalLit() *ledger.Entry {
	return &ledger.Entry{
		0,                           // SeqNo
		"",                          // ID
		"fixture-event-id",          // EventID
		"fixture.event.v1",          // EventType
		"fixture-actor",             // ActorID
		"",                          // SubjectID
		"",                          // TenantID
		"",                          // SessionID
		"fabricated-correlation-id", // CorrelationID — VIOLATION (positional idx 8)
		"fabricated-trace-id",       // TraceID — VIOLATION (positional idx 9)
		time.Time{},                 // OccurredAt
		time.Time{},                 // Timestamp
		nil,                         // Payload
		"",                          // PrevHash
		"",                          // Hash
	}
}

// badAssignment fabricates a TraceID value via a direct assignment statement.
// This is violation shape #3: an AssignStmt LHS write of ledger.Entry.TraceID
// from a file that is not in the injection allowlist. This shape simulates the
// scenario the reconstruction-granularity tightening is designed to catch: code
// inside a postgres-reconstruction-like file that adds a value-assignment
// alongside legitimate scan-address takes.
func badAssignment() *ledger.Entry {
	e := &ledger.Entry{}
	e.TraceID = "fabricated-trace-id" // VIOLATION: direct assignment outside allowlist
	return e
}

// badAssignmentCorrelation fabricates a CorrelationID value via a direct
// assignment statement — the CorrelationID counterpart of badAssignment, so the
// red fixture proves the scanner catches BOTH guarded fields on the assignment
// path (F4: previously only TraceID was exercised).
func badAssignmentCorrelation() *ledger.Entry {
	e := &ledger.Entry{}
	e.CorrelationID = "fabricated-correlation-id" // VIOLATION: direct assignment outside allowlist
	return e
}

// addressTake takes &e.TraceID and passes it to a Scan-like function. This is
// the blind-spot #1 shape: a UnaryExpr & applied to a SelectorExpr in a
// function-call argument position. The scanner checks AssignStmt LHS and
// CompositeLit Elts — neither matches this shape, so it is NOT flagged. This
// function exists to confirm the shape is present in the fixture but does not
// produce a violation.
func addressTake(scan func(...any)) *ledger.Entry {
	e := &ledger.Entry{}
	scan(&e.TraceID) // NOT a violation: UnaryExpr & scan address, not an assignment LHS
	return e
}
