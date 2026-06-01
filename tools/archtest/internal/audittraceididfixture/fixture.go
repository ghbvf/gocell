//go:build archtest_fixture

// Package audittraceididfixture is the AUDIT-TRACE-ID-WRITE-CALLER-01
// negative fixture. It constructs a ledger.Entry composite literal that
// explicitly sets the TraceID field from a non-appender, non-reconstruction
// context — the exact fabrication shape the rule prohibits.
//
// Loaded only when the archtest_fixture build tag is set. Never imported
// from production code. The scanner pointed at this package must report
// exactly one violation (the composite literal write in badWrite). A
// second function (addressTake) takes &e.TraceID for a Scan call; that
// shape is a blind-spot the rule cannot distinguish from a value write at
// the AssignStmt level — documented as a reconstruction scan-target blind
// spot in the archtest godoc.
//
// DO NOT use this package in production code.
package audittraceididfixture

import "github.com/ghbvf/gocell/runtime/audit/ledger"

// badWrite fabricates a ledger.Entry with an explicit TraceID value
// outside the sanctioned appender injection path. This is the violation
// shape: a composite literal field write of ledger.Entry.TraceID from a
// file that is not cells/auditcore/internal/appender/service.go.
func badWrite() *ledger.Entry {
	return &ledger.Entry{
		EventID:   "fixture-event-id",
		EventType: "fixture.event.v1",
		ActorID:   "fixture-actor",
		TraceID:   "fabricated-trace-id", // VIOLATION: non-appender TraceID injection
	}
}
