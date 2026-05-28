package journal

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// MemJournal is an in-memory Journal implementation for tests and development.
// It is not a production substrate — the PostgreSQL-backed implementation
// (PR-04) owns the production path. Single sync.Mutex serializes all state
// mutations; this is acceptable at dev/test scale.
//
// Two truths are kept consistent under the lock:
//   - The event log (events map) is append-only source of truth for history.
//   - The projection (instanceRow) is the authoritative coordination view:
//     current status, event version counter, and lease fencing token.
type MemJournal struct {
	clock clock.Clock

	mu        sync.Mutex
	instances map[idutil.SafeID]*instanceRow
	events    map[idutil.SafeID][]Event
}

// instanceRow holds both the saga state machine projection and the lease fence
// for a single saga instance. All fields are owned by MemJournal and mutated
// only while mu is held.
type instanceRow struct {
	inst           saga.Instance
	currentVersion int64
	leaseID        idutil.SafeID
	leaseExpiresAt time.Time
}

// NewMemJournal constructs a MemJournal. clk is a required dependency; a nil
// or typed-nil Clock panics via clock.MustHaveClock (programmer error,
// analogous to MustHaveClock convention across kernel/ wiring points). Returns
// (*MemJournal, nil) on success; the nil error satisfies the Journal-constructor
// convention even though the only failure mode panics.
func NewMemJournal(clk clock.Clock) (*MemJournal, error) {
	clock.MustHaveClock(clk, "saga journal: NewMemJournal requires non-nil Clock")
	return &MemJournal{
		clock:     clk,
		instances: make(map[idutil.SafeID]*instanceRow),
		events:    make(map[idutil.SafeID][]Event),
	}, nil
}

// Enqueue implements Journal.Enqueue.
func (m *MemJournal) Enqueue(_ context.Context, instance saga.Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.instances[instance.ID]; exists {
		return errDuplicateInstance(instance.ID)
	}
	if err := instance.ValidateNew(); err != nil {
		return err
	}
	m.instances[instance.ID] = &instanceRow{inst: instance, currentVersion: 0}
	m.events[instance.ID] = []Event{}
	return nil
}

// Append implements Journal.Append.
func (m *MemJournal) Append(_ context.Context, instanceID, leaseID idutil.SafeID, event Event) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.instances[instanceID]
	if !ok {
		return 0, errInstanceNotFound(instanceID)
	}

	now := m.clock.Now()
	if !m.fenced(row, leaseID, now) {
		return 0, errStaleLease(instanceID, leaseID)
	}

	if err := event.ValidateForAppend(); err != nil {
		return 0, err
	}

	// STATUS PROJECTION: advance the instance status as a deterministic function
	// of the event kind (fail-closed on an out-of-phase event), reusing the saga
	// state machine. See Journal.Append godoc for the full rule set.
	if err := m.applyProjection(row, instanceID, event.Kind, now); err != nil {
		return 0, err
	}

	version := m.appendLocked(row, instanceID, Event{
		Kind:      event.Kind,
		StepName:  event.StepName,
		Payload:   append([]byte(nil), event.Payload...),
		Version:   0, // filled by appendLocked
		CreatedAt: now,
	})
	return version, nil
}

// Load implements Journal.Load.
func (m *MemJournal) Load(_ context.Context, instanceID idutil.SafeID) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.instances[instanceID]; !ok {
		return nil, errInstanceNotFound(instanceID)
	}

	src := m.events[instanceID]
	// Return a deep copy so callers cannot mutate journal state.
	out := make([]Event, len(src))
	for i, e := range src {
		out[i] = e
		if e.Payload != nil {
			out[i].Payload = append([]byte(nil), e.Payload...)
		}
	}
	return out, nil
}

// ClaimPending implements Journal.ClaimPending.
func (m *MemJournal) ClaimPending(_ context.Context, batchSize int, leaseDuration time.Duration) ([]ClaimedInstance, idutil.SafeID, error) {
	// Reject non-positive batchSize / leaseDuration before taking the lock.
	if batchSize <= 0 {
		return nil, "", errNonPositiveBatchSize(batchSize)
	}
	if leaseDuration <= 0 {
		return nil, "", errNonPositiveLeaseDuration(leaseDuration)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Fix C: read clock inside the lock so now and projection reads/writes are
	// atomic under FakeClock concurrent Advance.
	now := m.clock.Now()

	// Collect claimable candidates: non-terminal AND (unleased OR expired lease).
	type candidate struct {
		id  idutil.SafeID
		row *instanceRow
	}
	var candidates []candidate
	for id, row := range m.instances {
		if row.inst.Status.IsTerminal() {
			continue
		}
		if row.leaseID != "" && !row.leaseExpiresAt.Before(now) {
			continue // active lease held by someone else
		}
		candidates = append(candidates, candidate{id: id, row: row})
	}

	// Stable deterministic ordering: sort by (StartedAt ASC, ID ASC).
	sort.Slice(candidates, func(i, j int) bool {
		si := candidates[i].row.inst.StartedAt
		sj := candidates[j].row.inst.StartedAt
		if si.Equal(sj) {
			return candidates[i].id < candidates[j].id
		}
		return si.Before(sj)
	})

	if len(candidates) > batchSize {
		candidates = candidates[:batchSize]
	}

	if len(candidates) == 0 {
		return nil, "", nil
	}

	// Fix I: mint UUID only when there is ≥1 candidate to lease.
	s, err := idutil.NewUUID()
	if err != nil {
		return nil, "", err
	}
	leaseID := idutil.SafeID(s)

	claimed := make([]ClaimedInstance, 0, len(candidates))
	for _, c := range candidates {
		c.row.leaseID = leaseID
		c.row.leaseExpiresAt = now.Add(leaseDuration)
		claimed = append(claimed, ClaimedInstance{
			// Fix B: deep-copy *time.Time pointer fields so caller mutations
			// cannot corrupt journal projection through shared pointers.
			Instance: cloneInstance(c.row.inst),
			LeaseID:  leaseID,
		})
	}
	return claimed, leaseID, nil
}

// Heartbeat implements Journal.Heartbeat.
func (m *MemJournal) Heartbeat(_ context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error) {
	if leaseDuration <= 0 {
		return false, errNonPositiveLeaseDuration(leaseDuration)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.instances[instanceID]
	if !ok {
		return false, nil
	}

	now := m.clock.Now()
	if !m.fenced(row, leaseID, now) {
		return false, nil
	}

	row.leaseExpiresAt = now.Add(leaseDuration)
	return true, nil
}

// MarkTerminal implements Journal.MarkTerminal.
func (m *MemJournal) MarkTerminal(_ context.Context, instanceID, leaseID idutil.SafeID, finalStatus saga.Status) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, ok := m.instances[instanceID]
	if !ok {
		return false, nil
	}

	now := m.clock.Now()
	if !m.fenced(row, leaseID, now) {
		return false, nil
	}

	if !finalStatus.IsTerminal() {
		return false, errInvalidTerminalStatus(instanceID, row.inst.Status, finalStatus)
	}

	// Map the terminal status to its event kind so the log encodes the final
	// state (Load alone replays which terminal was reached). IsTerminal() above
	// guarantees ok; the guard is defensive.
	termKind, okKind := TerminalEventKind(finalStatus)
	if !okKind {
		return false, errInvalidTerminalStatus(instanceID, row.inst.Status, finalStatus)
	}

	// AdvanceSaga re-validates the transition and surfaces illegal-transition
	// errors (KindInvalid+ErrValidationFailed).
	if err := saga.AdvanceSaga(&row.inst, finalStatus, now); err != nil {
		return false, err
	}

	// Append the closing terminal event atomically with the status flip.
	m.appendLocked(row, instanceID, Event{
		Kind:      termKind,
		StepName:  "",
		Payload:   nil,
		Version:   0, // filled by appendLocked
		CreatedAt: now,
	})

	// Release the lease on success.
	row.leaseID = ""
	return true, nil
}

// RepoReady implements healthz.RepoProber. The in-memory store has no
// differentiated failure domain; it always returns nil.
func (m *MemJournal) RepoReady(_ context.Context) error {
	return nil
}

// ---------------------------------------------------------------------------
// Private helpers
// ---------------------------------------------------------------------------

// fenced reports whether leaseID is the current valid lease on row as of now.
// A row is fenced-in (returns true) when its leaseID matches AND the lease has
// not expired. Called with m.mu held.
func (m *MemJournal) fenced(row *instanceRow, leaseID idutil.SafeID, now time.Time) bool {
	if row.leaseID == "" {
		return false
	}
	if row.leaseID != leaseID {
		return false
	}
	return !row.leaseExpiresAt.Before(now)
}

// appendLocked appends a pre-built Event to the event log, assigns it the next
// version number, sets its Version field, and bumps row.currentVersion.
// The event's Version and CreatedAt are expected to be set by the caller
// (CreatedAt to now, Version to 0 as a placeholder). Returns the assigned version.
// Must be called with m.mu held.
func (m *MemJournal) appendLocked(row *instanceRow, instanceID idutil.SafeID, e Event) int64 {
	version := row.currentVersion + 1
	e.Version = version
	m.events[instanceID] = append(m.events[instanceID], e)
	row.currentVersion = version
	return version
}

// applyProjection advances the instance's non-terminal status as a deterministic
// function of the appended event kind, reusing the saga state machine. Called
// under m.mu before the event is recorded; returns an error WITHOUT mutating on
// an out-of-phase event or illegal transition (fail-closed). Terminal kinds never
// reach here — ValidateForAppend rejects them (they go through MarkTerminal).
//
// Phase rules (the projection is a fold of the event log):
//   - forward step (Started/Completed/Failed): Pending → Running (starts the
//     saga); legal-but-no-op while Running; rejected while Compensating.
//   - KindCompensationStarted: Running → Compensating (AdvanceSaga rejects
//     Pending→Compensating and a repeated Compensating→Compensating).
//   - KindStepCompensated: legal only while Compensating; status unchanged.
func (m *MemJournal) applyProjection(row *instanceRow, instanceID idutil.SafeID, kind EventKind, now time.Time) error {
	st := row.inst.Status
	switch kind {
	case KindStepStarted, KindStepCompleted, KindStepFailed:
		switch st {
		case saga.StatusPending:
			return saga.AdvanceSaga(&row.inst, saga.StatusRunning, now)
		case saga.StatusRunning:
			return nil
		default:
			return errEventPhase(instanceID, kind, st)
		}
	case KindCompensationStarted:
		return saga.AdvanceSaga(&row.inst, saga.StatusCompensating, now)
	case KindStepCompensated, KindStepCompensationFailed:
		// Both compensate outcomes are legal only while Compensating; status
		// unchanged. KindStepCompensationFailed (#1181) is the per-step
		// failure variant of KindStepCompensated — distinct so the log
		// projects reverse-walk progress without overloading KindStepFailed
		// (which the Running-phase projection accepts but Compensating
		// rejects).
		if st != saga.StatusCompensating {
			return errEventPhase(instanceID, kind, st)
		}
		return nil
	default:
		return nil
	}
}

// cloneInstance deep-copies the pointer fields of a saga.Instance so a returned
// projection cannot be mutated through shared *time.Time pointers.
func cloneInstance(in saga.Instance) saga.Instance {
	out := in
	if in.UpdatedAt != nil {
		t := *in.UpdatedAt
		out.UpdatedAt = &t
	}
	if in.CompletedAt != nil {
		t := *in.CompletedAt
		out.CompletedAt = &t
	}
	return out
}
