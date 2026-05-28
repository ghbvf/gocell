package projection

import "fmt"

// Phase is the lifecycle state of a projection, exposed by the Coordinator
// (PR-03) so operators (readyz / metrics) and business read paths can observe
// where a projection is. The four rebuild phases mirror Axon's processor
// lifecycle (Stop → Reset → Replay → Catch-up); PhaseLive is the steady-state
// value outside a rebuild.
//
// # Business read-503 opt-in
//
// rebuild does NOT block business reads by default — stale reads during rebuild
// are the industry consensus (Axon / Marten / Commanded). A business read path
// MAY consult Phase() and return 503 of its own accord; the harness never forces
// it. PhaseLive means the read-model is fresh and serving; any other phase means
// a rebuild is in progress. See ADR §5.
//
// # Frozen membership
//
// The const set is frozen by archtest PROJECTION-STATE-PHASE-FROZEN-01 (the
// operational contract for readyz/metrics/503 must not drift silently). The
// rebuild state machine + transition table that drive these phases land in
// PR-03.
//
// ref: kernel/outbox/state.go (enum + String pattern).
// ref: AxonFramework EventProcessor lifecycle (Stop/Reset/Replay/Catch-up).
type Phase uint8

const (
	// PhaseLive: steady-state consuming, read-model fresh and serving.
	//
	// IMPORTANT: iota+1 makes the zero value (0) an invalid Phase, so a
	// forgotten/uninitialised Phase cannot silently appear as PhaseLive.
	PhaseLive Phase = iota + 1 // = 1
	// PhaseStopped: rebuild step 1 — consume loop halted, checkpoint claim
	// released so the reset/replay can rewrite the offset row safely.
	PhaseStopped
	// PhaseReset: rebuild step 2 — business OnReset hook (TRUNCATE/DROP its
	// read-model) + harness resets the checkpoint offset to 0, one CellTx.
	PhaseReset
	// PhaseReplay: rebuild step 3 — replaying the event stream from offset 0,
	// each event in its own CellTx (Apply + checkpoint).
	PhaseReplay
	// PhaseCatchup: rebuild step 4 — replay reached the head offset captured at
	// rebuild start; now consuming newly arriving events until caught up, then
	// transition back to PhaseLive.
	PhaseCatchup
)

// Valid reports whether p is a recognized Phase value.
func (p Phase) Valid() bool {
	return p >= PhaseLive && p <= PhaseCatchup
}

// String returns the canonical wire/log label for p. This is the sole producer
// of the phase label used in metrics/readyz; the literals are frozen by
// PROJECTION-STATE-PHASE-FROZEN-01.
func (p Phase) String() string {
	switch p {
	case 0:
		return "invalid"
	case PhaseLive:
		return "live"
	case PhaseStopped:
		return "stopped"
	case PhaseReset:
		return "reset"
	case PhaseReplay:
		return "replay"
	case PhaseCatchup:
		return "catchup"
	default:
		return fmt.Sprintf("phase(%d)", uint8(p))
	}
}
