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
		return errDuplicateInstance()
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
		return 0, errInstanceNotFound()
	}

	now := m.clock.Now()
	if !m.fenced(row, leaseID, now) {
		return 0, errStaleLease()
	}

	if err := event.ValidateForAppend(); err != nil {
		return 0, err
	}

	// STATUS PROJECTION: advance the instance status as a deterministic function
	// of the event kind, reusing the saga state machine so every move is
	// transition-validated. See Journal.Append godoc for the full rule set.
	if target, needsTransition := m.projectionTarget(row.inst.Status, event.Kind); needsTransition {
		if err := saga.AdvanceSaga(&row.inst, target, now); err != nil {
			return 0, err
		}
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
		return nil, errInstanceNotFound()
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
	now := m.clock.Now()

	s, err := idutil.NewUUID()
	if err != nil {
		return nil, "", err
	}
	leaseID := idutil.SafeID(s)

	m.mu.Lock()
	defer m.mu.Unlock()

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

	if batchSize > 0 && len(candidates) > batchSize {
		candidates = candidates[:batchSize]
	}

	if len(candidates) == 0 {
		return nil, "", nil
	}

	claimed := make([]ClaimedInstance, 0, len(candidates))
	for _, c := range candidates {
		c.row.leaseID = leaseID
		c.row.leaseExpiresAt = now.Add(leaseDuration)
		claimed = append(claimed, ClaimedInstance{
			Instance: c.row.inst, // copy of the struct value
			LeaseID:  leaseID,
		})
	}
	return claimed, leaseID, nil
}

// Heartbeat implements Journal.Heartbeat.
func (m *MemJournal) Heartbeat(_ context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error) {
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
		return false, errInvalidTerminalStatus()
	}
	if err := saga.Transition(row.inst.Status, finalStatus); err != nil {
		return false, errInvalidTerminalStatus()
	}

	if err := saga.AdvanceSaga(&row.inst, finalStatus, now); err != nil {
		return false, err
	}

	// Append the closing KindSagaTerminal event atomically with the status flip.
	m.appendLocked(row, instanceID, Event{
		Kind:      KindSagaTerminal,
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

// projectionTarget returns the status the projection should advance to given the
// current status and the incoming event kind, and whether any transition is needed.
// The mapping implements the status-projection rules in Journal.Append godoc.
func (m *MemJournal) projectionTarget(current saga.Status, kind EventKind) (saga.Status, bool) {
	switch {
	case current == saga.StatusPending && kind != KindStepCompensated:
		// First forward step event on a Pending instance: Pending → Running.
		return saga.StatusRunning, true
	case current == saga.StatusRunning && kind == KindStepCompensated:
		// Compensation event on a Running instance: Running → Compensating.
		return saga.StatusCompensating, true
	case current == saga.StatusPending && kind == KindStepCompensated:
		// Compensation on a Pending instance is illegal. Return Compensating as the
		// target so that AdvanceSaga surfaces the illegal Pending→Compensating
		// transition error. Fail-closed: the error from AdvanceSaga propagates.
		return saga.StatusCompensating, true
	default:
		return 0, false
	}
}
