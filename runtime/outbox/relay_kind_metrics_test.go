// Package outbox — white-box tests for the relay's command-vs-event metric/log
// attribution (#1674 F4/F5). They live in package outbox to reach pollOnce, the
// minimalStore harness, and the unexported command-dispatch wiring.
//
// The keystone discriminator is publishResult.isCommand, set by publishBatch's
// commandDispatchFor branch. These drive the FULL poll cycle (claim → dispatch →
// writeBack) so the attribution is exercised end-to-end, not via a hand-set marker.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/runtime/command"
)

// kindMetricsCollector captures PollCycleResults so a test can assert the per-kind
// (event vs command) outcome attribution the relay produced.
type kindMetricsCollector struct {
	mu     sync.Mutex
	cycles []kout.PollCycleResult
}

func (c *kindMetricsCollector) RecordPollCycle(_ context.Context, r kout.PollCycleResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cycles = append(c.cycles, r)
}
func (c *kindMetricsCollector) RecordBatchSize(context.Context, int)        {}
func (c *kindMetricsCollector) RecordReclaim(context.Context, int64)        {}
func (c *kindMetricsCollector) RecordCleanup(context.Context, int64, int64) {}

func (c *kindMetricsCollector) totals() (event, cmd kout.OutcomeCounts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.cycles {
		event.Published += r.Event.Published
		event.Retried += r.Event.Retried
		event.Dead += r.Event.Dead
		event.Skipped += r.Event.Skipped
		event.Lost += r.Event.Lost
		cmd.Published += r.Command.Published
		cmd.Retried += r.Command.Retried
		cmd.Dead += r.Command.Dead
		cmd.Skipped += r.Command.Skipped
		cmd.Lost += r.Command.Lost
	}
	return event, cmd
}

// TestPollOnce_CommandSettlesToCommandKind is the F4 keystone: a command entry
// driven through the full poll settles into the COMMAND bucket of PollCycleResult
// (→ outbox_relayed_total{kind="command"}), never the event bucket. Fails until
// publishBatch marks the command-dispatch branch isCommand.
func TestPollOnce_CommandSettlesToCommandKind(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{"deviceId":"d1"}`)

	mc := &kindMetricsCollector{}
	cfg := RelayConfig{}.WithDefaults()
	cfg.Metrics = mc
	r := NewRelay(clock.Real(), store, &recordingPublisher{}, cfg)
	r.WithCommandDispatch(command.NewRegistry(), map[command.CommandID]command.AsyncDispatchFunc{
		cmdID: func(context.Context, *command.Registry, kout.Entry) error { return nil },
	}, newCommandClaimer())

	require.NoError(t, r.pollOnce(context.Background()))

	event, cmd := mc.totals()
	assert.Equal(t, 1, cmd.Published, "command dispatch must settle to the command kind bucket")
	assert.Zero(t, event, "a command must NOT be attributed to the event bucket")
}

// TestPollOnce_EventSettlesToEventKind is the control: a plain event entry settles
// into the EVENT bucket. Guards against over-attributing everything to command.
func TestPollOnce_EventSettlesToEventKind(t *testing.T) {
	t.Parallel()
	store := newMinimalStore()
	store.seedPending("e1") // topic "ev": not a registered command → broker/event path

	mc := &kindMetricsCollector{}
	cfg := RelayConfig{}.WithDefaults()
	cfg.Metrics = mc
	// No WithCommandDispatch: the topic is not a registered command → broker path.
	r := NewRelay(clock.Real(), store, &recordingPublisher{}, cfg)

	require.NoError(t, r.pollOnce(context.Background()))

	event, cmd := mc.totals()
	assert.Equal(t, 1, event.Published, "event publish must settle to the event kind bucket")
	assert.Zero(t, cmd, "an event must NOT be attributed to the command bucket")
}

// attrCaptureHandler is a minimal slog.Handler that records emitted records so a
// test can assert structured attributes. Safe for the serial, synchronous pollOnce
// drive below (no async writes — see slogcapture.InstallDefault contract).
type attrCaptureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *attrCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *attrCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *attrCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *attrCaptureHandler) WithGroup(string) slog.Handler      { return h }

// findAttrs returns the attributes of the first captured record with msg, or nil.
func (h *attrCaptureHandler) findAttrs(msg string) map[string]slog.Value {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Message != msg {
			continue
		}
		out := map[string]slog.Value{}
		rec.Attrs(func(a slog.Attr) bool {
			out[a.Key] = a.Value
			return true
		})
		return out
	}
	return nil
}

// TestPollOnce_CommandDeadLetter_LogsIsCommand is the F5 keystone: when a command
// dispatch fails permanently and the row dead-letters, the dead-letter log carries
// is_command=true (+ command_id). Fails until publishBatch marks the command branch
// isCommand. Serial (no t.Parallel) — slogcapture.InstallDefault mutates the global
// default and pollOnce emits the log synchronously in this goroutine.
func TestPollOnce_CommandDeadLetter_LogsIsCommand(t *testing.T) {
	const cmdID = "command.test.fail.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	h := &attrCaptureHandler{}
	slogcapture.InstallDefault(t, slog.New(h))

	r := NewRelay(clock.Real(), store, &recordingPublisher{}, RelayConfig{}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(), map[command.CommandID]command.AsyncDispatchFunc{
		// Permanent error → handleFailedEntry dead-letters on the first failure.
		cmdID: func(context.Context, *command.Registry, kout.Entry) error {
			return kout.NewPermanentError(errors.New("unrecoverable command"))
		},
	}, newCommandClaimer())

	require.NoError(t, r.pollOnce(context.Background()))

	store.mu.Lock()
	status := store.rows["c1"].status
	store.mu.Unlock()
	require.Equal(t, "dead", status, "permanent command failure must dead-letter")

	attrs := h.findAttrs("outbox relay: entry dead-lettered")
	require.NotNil(t, attrs, "dead-letter log must be emitted")
	isCmd, ok := attrs["is_command"]
	require.True(t, ok, "dead-letter log must carry an is_command attribute")
	assert.True(t, isCmd.Bool(), "command dead-letter must log is_command=true")
	assert.Equal(t, "cmd-c1", attrs["command_id"].String(), "command dead-letter must surface command_id")
}
