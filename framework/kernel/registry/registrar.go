package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// ContractRegistrar is the runtime, mutable contract-registration state machine
// (303-US2, #2233). Unlike the read-only ContractRegistry / CellRegistry (which
// are populated once at bootstrap and immutable thereafter), the registrar
// accepts runtime-submitted contracts and drives each through the sealed
// RegistrationState machine via Submit / Advance.
//
// It keeps two structures consistent under a single mutex (mirroring
// kernel/saga/journal.MemJournal's two-truth model): an append-only per-id
// RegistrationEvent log (source of truth for history) and an in-mem projection
// index (current state per registration). Advance validates the transition
// BEFORE appending, so an illegal transition leaves both structures untouched
// (fail-closed, no half-write). A plain sync.Mutex serializes all access —
// reads return value copies, so there is no lock-upgrade hazard.
type ContractRegistrar struct {
	clk    clock.Clock
	mu     sync.Mutex
	events map[string][]RegistrationEvent
	index  map[string]*ContractRegistration
}

// NewContractRegistrar builds an empty registrar. clk is a required positional
// dependency (clock.Clock convention); it stamps event timestamps inside the lock.
func NewContractRegistrar(clk clock.Clock) *ContractRegistrar {
	clock.MustHaveClock(clk, "registry.NewContractRegistrar")
	return &ContractRegistrar{
		clk:    clk,
		events: make(map[string][]RegistrationEvent),
		index:  make(map[string]*ContractRegistration),
	}
}

// Submit records a new registration in the submitted state, appending the
// initial migration event (From = zero sentinel, To = submitted). It returns
// ErrValidationFailed for missing required fields and ErrRegistrationDuplicate
// if the id already exists. dedup is by registration id only; the
// (kind,domain,version,owner) uniqueness quadruple is owned by US15/FR-009.
func (r *ContractRegistrar) Submit(in SubmitInput) (ContractRegistration, error) {
	if err := in.validate(); err != nil {
		return ContractRegistration{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.index[in.ID]; exists {
		return ContractRegistration{}, errcode.New(errcode.KindConflict, errcode.ErrRegistrationDuplicate,
			"registry: registration id already exists",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%q", in.ID))))
	}
	now := r.clk.Now()
	reg := &ContractRegistration{
		ID:            in.ID,
		Kind:          in.Kind,
		PayloadSchema: in.PayloadSchema,
		Submitter:     in.Submitter,
		State:         stateSubmitted,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	r.index[in.ID] = reg
	r.appendLocked(in.ID, RegistrationEvent{
		From:       RegistrationState{},
		To:         stateSubmitted,
		Actor:      in.Submitter,
		OccurredAt: now,
	})
	return *reg, nil
}

// Advance moves a registration from its current state to `to`, recording one
// migration event. It returns ErrRegistrationNotFound for an unknown id and
// ErrRegistrationInvalidTransition for a transition the legalTransitions table
// forbids (including any transition out of a terminal state, a self-loop, or a
// forged zero target). The transition is validated before any mutation, so a
// rejected Advance leaves the projection and event log untouched. When `to` is
// approved, the actor is recorded as the registration's Approver.
func (r *ContractRegistrar) Advance(id string, to RegistrationState, actor, reason string) (ContractRegistration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.index[id]
	if !ok {
		return ContractRegistration{}, errcode.New(errcode.KindNotFound, errcode.ErrRegistrationNotFound,
			"registry: registration not found",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%q", id))))
	}
	if err := Transition(cur.State, to); err != nil {
		return ContractRegistration{}, err
	}
	now := r.clk.Now()
	from := cur.State
	cur.State = to
	cur.UpdatedAt = now
	if to == stateApproved {
		cur.Approver = actor
	}
	r.appendLocked(id, RegistrationEvent{
		From:       from,
		To:         to,
		Actor:      actor,
		Reason:     reason,
		OccurredAt: now,
	})
	return *cur, nil
}

// appendLocked appends e to id's event stream, assigning the owning id and the
// 1-based contiguous Seq. Caller must hold r.mu.
func (r *ContractRegistrar) appendLocked(id string, e RegistrationEvent) {
	e.RegistrationID = id
	e.Seq = len(r.events[id]) + 1
	r.events[id] = append(r.events[id], e)
}

// Get returns a copy of the registration by id, or (zero, false) if not found.
// The copy isolates callers from the projection (registration is all value
// types, so the struct copy is a full deep copy).
func (r *ContractRegistrar) Get(id string) (ContractRegistration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.index[id]
	if !ok {
		return ContractRegistration{}, false
	}
	return *reg, true
}

// ByState returns copies of all registrations currently in the given state,
// sorted by id. The set is derived by filtering the projection on read (no
// separate byState index to keep consistent — registration counts are small and
// the mutable index makes a maintained bucket map a needless correctness risk).
func (r *ContractRegistrar) ByState(state RegistrationState) []ContractRegistration {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ContractRegistration
	for _, reg := range r.index {
		if reg.State == state {
			out = append(out, *reg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Events returns a deep copy of id's append-only migration event stream (ordered
// by Seq), or (nil, false) if the id is unknown.
func (r *ContractRegistrar) Events(id string) ([]RegistrationEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	evs, ok := r.events[id]
	if !ok {
		return nil, false
	}
	return append([]RegistrationEvent(nil), evs...), true
}

// Count returns the number of registrations.
func (r *ContractRegistrar) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.index)
}

// AllIDs returns all registration ids sorted alphabetically.
func (r *ContractRegistrar) AllIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.index))
	for id := range r.index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
