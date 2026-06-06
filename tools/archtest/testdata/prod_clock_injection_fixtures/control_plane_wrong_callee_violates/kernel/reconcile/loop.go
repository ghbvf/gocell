// Package reconcile is a RED fixture for PROD-CLOCK-INJECTION-01 #1136 review F2.
//
// It asserts that the (host, method, callee) exact-triple carve-out rejects a
// sanctioned controlPlaneClock method that calls the WRONG time.* function.
// Under the receiver-type-only form, time.Sleep inside
// controlPlaneClock.newRenewTicker would have been exempted; the host-scoped
// table rejects it because the kernel/reconcile "newRenewTicker" callee is
// "NewTicker", not "Sleep".
package reconcile

import "time"

type controlPlaneClock struct{}

// newRenewTicker is the sanctioned method — but here it calls the WRONG callee.
// The (method, callee) pair lock catches the mismatch.
func (controlPlaneClock) newRenewTicker(d time.Duration) *time.Ticker {
	time.Sleep(d)            // RED: Sleep is not the sanctioned callee for newRenewTicker
	return time.NewTicker(d) // sanctioned callee — not flagged
}
