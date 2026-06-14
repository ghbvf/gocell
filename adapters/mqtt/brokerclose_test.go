package mqtt

import (
	"log/slog"
	"testing"
	"time"

	mqttserver "github.com/mochi-mqtt/server/v2"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// CloseBrokerSafely is the single shared helper that closes a mochi v2 broker
// across both the internal (package mqtt) and external (package mqtt_test) test
// packages in this directory. It is exported only so the external test package
// can reach it via mqtt.CloseBrokerSafely; it never appears in non-test builds.
//
// It applies two defenses against the mochi v2.7.9 Clients-lock deadlock
// (issue #1315 — there is no fixed upstream release; v2.7.9 is the latest):
//
//  1. Best-effort drain (quiesce-before-Close): when t is non-nil, polls
//     srv.Clients.Len() == 0 for up to testtime.D2s so any in-flight client
//     disconnects settle before Close is called — this closes the deadlock
//     window. Drain is non-fatal; if the budget expires the guarded close
//     proceeds regardless.
//
//  2. Timeout-guarded close: runs srv.Close() in a goroutine and selects on
//     done vs testtime.D5s. If Close returns, we're done. If it exceeds the
//     budget (the mochi Clients-lock deadlock) a diagnostic is logged and the
//     helper returns promptly so teardown completes instead of hanging for
//     ~10 min. The timeout path only logs (it does not fail the test): a
//     teardown Close deadlock is a known library bug, not a real-test-assertion
//     failure, so failing here would trade a rare hang for a rare flaky FAIL.
//
// t may be nil: the shared-broker stop closure (initSharedInternalBroker)
// captures CloseBrokerSafely(nil, srv) and is invoked from TestMain via
// stopSharedInternalBroker after m.Run(). In that case drain is skipped and
// slog.Warn is used instead of t.Logf so the goroutine does not reference a
// finished test.
func CloseBrokerSafely(t testing.TB, srv *mqttserver.Server) {
	if t != nil {
		t.Helper()
	}

	// Defense 1: best-effort drain (non-fatal on timeout).
	// Only attempted when a live test handle is present, using a manual
	// timer+ticker loop — not testwait.External — so drain timeout does not
	// call t.Fatalf and abort the test.
	if t != nil {
		drainTimer := time.NewTimer(testtime.D2s)
		drainTick := time.NewTicker(testtime.D10ms)
	drainLoop:
		for srv.Clients.Len() > 0 {
			select {
			case <-drainTimer.C:
				break drainLoop
			case <-drainTick.C:
				// tick: re-check srv.Clients.Len() on the next iteration.
			}
		}
		drainTimer.Stop()
		drainTick.Stop()
	}

	// Defense 2: timeout-guarded close.
	done := make(chan error, 1)
	go func() { done <- srv.Close() }()
	select {
	case err := <-done:
		if err != nil {
			if t != nil {
				t.Logf("mqtt-test: srv.Close() returned error: %v", err)
			} else {
				slog.Warn("mqtt-test: srv.Close() returned error", "err", err)
			}
		}
	case <-time.After(testtime.D5s):
		const msg = "mqtt-test: srv.Close() exceeded 5s — suspected mochi v2.7.9 Clients-lock deadlock; abandoning close"
		if t != nil {
			t.Log(msg)
		} else {
			slog.Warn(msg)
		}
	}
}
