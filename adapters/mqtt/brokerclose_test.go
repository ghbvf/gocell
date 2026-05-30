package mqtt

import (
	"log/slog"
	"testing"
	"time"

	mqttserver "github.com/mochi-mqtt/server/v2"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// closeBrokerSafely closes a mochi v2 broker with two defenses against the
// mochi v2.7.9 Clients-lock deadlock:
//
//  1. Best-effort drain: when t is non-nil, polls srv.Clients.Len() == 0 for
//     up to testtime.D2s so any in-flight client disconnects settle before
//     Close is called. Drain is non-fatal; if the budget expires the guarded
//     close proceeds regardless.
//
//  2. Timeout-guarded close: runs srv.Close() in a goroutine and selects on
//     done vs testtime.D5s. If Close returns, we're done. If it exceeds the
//     budget (the mochi Clients-lock deadlock) a diagnostic is logged and the
//     helper returns so the test fails fast instead of hanging for ~10 min.
//
// t may be nil when called from TestMain after m.Run() (e.g. the shared
// broker stop path). In that case drain is skipped and slog.Warn is used
// instead of t.Logf so the goroutine does not reference a finished test.
func closeBrokerSafely(t testing.TB, srv *mqttserver.Server) {
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
