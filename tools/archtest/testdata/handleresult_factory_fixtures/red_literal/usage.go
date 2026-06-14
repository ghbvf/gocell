// Package red_literal is a RED fixture for OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01:
// a business-side outbox.HandleResult{} composite literal outside the
// kernel/outbox allowlist must be flagged with a STRUCTURED Rel/Line diagnostic
// (codex #1682 F6 — the previous diagnostic left Rel/Line empty, so Report
// printed a bogus ":0:" prefix with the location duplicated inside the message).
package red_literal

import "github.com/ghbvf/gocell/framework/kernel/outbox"

// handle returns a bare outbox.HandleResult{} composite literal — the form
// business handlers must replace with outbox.Ack() / Requeue(err) / Reject(err).
func handle() outbox.HandleResult {
	return outbox.HandleResult{}
}
