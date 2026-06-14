package sagaprojection_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/kernel/saga/sagajournaltest"
	"github.com/ghbvf/gocell/framework/kernel/saga/sagaprojection"
)

// TestSagaProjectionEvent_PayloadCarriesKindEnvelope is the regression guard for
// the #1391 kernel-bridge fix: a saga-journal projection consumer MUST be able to
// recover the event Kind. Terminal events (MarkTerminal) carry Payload: nil, so
// the terminal status (succeeded/compensated/failed) lives ONLY in Event.Kind —
// which toCarrier previously dropped. The carrier now serializes a
// SagaEventEnvelope{kind, stepName, payload} into Payload().
//
// Without this, the orderfulfillment projection (#1391) cannot fold terminal
// status — the consumer would see {Payload: nil} with no way to tell succeeded
// from compensated.
func TestSagaProjectionEvent_PayloadCarriesKindEnvelope(t *testing.T) {
	t.Parallel()

	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr := j.(journal.GlobalReader)
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

	// Enqueue + claim a fresh instance, append a step event with a known payload,
	// then commit a terminal event (nil payload — kind is the only signal).
	instID := uniqueInstID()
	inst := sagajournaltest.NewInstanceFixture(t, instID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	_, leaseID, err := j.ClaimPending(context.Background(), 100, time.Hour)
	if err != nil || leaseID == "" {
		t.Fatalf("ClaimPending: err=%v leaseID=%v", err, leaseID)
	}

	const stepPayload = `{"step":"one"}`
	if _, err := j.Append(context.Background(), inst.ID, leaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
		Payload:  []byte(stepPayload),
	}); err != nil {
		t.Fatalf("Append step: %v", err)
	}
	ok, err := j.MarkTerminal(context.Background(), inst.ID, leaseID, sagaprojection.StatusSucceededForTest)
	if err != nil || !ok {
		t.Fatalf("MarkTerminal: ok=%v err=%v", ok, err)
	}

	var got []projection.ProjectionEvent
	if err := src.Replay(context.Background(), 0, func(e projection.ProjectionEvent) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("Replay delivered %d events, want >= 2 (step + terminal)", len(got))
	}

	// First event: the step. Envelope must carry kind + stepName + original payload.
	var stepEnv sagaprojection.SagaEventEnvelope
	if err := json.Unmarshal(got[0].Payload(), &stepEnv); err != nil {
		t.Fatalf("decode step envelope: %v (payload=%s)", err, got[0].Payload())
	}
	if stepEnv.Kind != journal.KindStepStarted.String() {
		t.Errorf("step envelope Kind = %q, want %q", stepEnv.Kind, journal.KindStepStarted.String())
	}
	if stepEnv.StepName != "step-one" {
		t.Errorf("step envelope StepName = %q, want %q", stepEnv.StepName, "step-one")
	}
	if string(stepEnv.Payload) != stepPayload {
		t.Errorf("step envelope Payload = %s, want %s (original step payload preserved)", stepEnv.Payload, stepPayload)
	}

	// Last event: the terminal. Kind MUST be recoverable even though the journal
	// Payload was nil — this is the whole point of the fix.
	var termEnv sagaprojection.SagaEventEnvelope
	if err := json.Unmarshal(got[len(got)-1].Payload(), &termEnv); err != nil {
		t.Fatalf("decode terminal envelope: %v (payload=%s)", err, got[len(got)-1].Payload())
	}
	if termEnv.Kind != journal.KindSagaSucceeded.String() {
		t.Errorf("terminal envelope Kind = %q, want %q — terminal status must be recoverable from the envelope",
			termEnv.Kind, journal.KindSagaSucceeded.String())
	}
	if kind, ok := journal.ParseEventKind(termEnv.Kind); !ok || kind != journal.KindSagaSucceeded {
		t.Errorf("ParseEventKind(%q) = (%d, %v), want (KindSagaSucceeded, true)", termEnv.Kind, kind, ok)
	}
}
