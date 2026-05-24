package saga

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// StepFunc executes one saga step body. ctx carries the instance's deadline;
// inst is read-only metadata (mutating it is a programmer error — the
// Coordinator owns transitions). prevState is the opaque JSON payload of the
// previous step's success event (nil for the first step). Returns new opaque
// JSON state, or a non-nil error to signal step failure.
type StepFunc func(ctx context.Context, inst *Instance, prevState []byte) (newState []byte, err error)

// CompensateFunc undoes one previously-committed step. MUST be pure-reverse:
// no outbox.Writer, no *sql.Tx (locked by SAGA-STEP-COMPENSATE-PURE-01
// archtest shipped in the same PR).
type CompensateFunc func(ctx context.Context, inst *Instance, committedState []byte) error

// Step is a single named unit inside a Definition. Name MUST be a valid
// SafeID and flows into journal Event.StepName.
type Step struct {
	Name       idutil.SafeID
	Run        StepFunc
	Compensate CompensateFunc // optional; nil => terminal-on-failure
	Timeout    time.Duration  // per-step; 0 => inherit Definition.Timeout
}

// Definition is the static recipe for a saga. ID is the DefinitionID stored
// on every Instance enrolled under this definition. Steps execute in slice
// order. Timeout is the overall ceiling (Expired) — PR-06 wires per-step
// retry; PR-03 stores the field for total-saga timeout enforcement (see
// Coordinator.driveOne).
//
// RetryPolicy field is intentionally absent. PR-06 adds it; addition is
// forward-compatible (new optional field).
type Definition struct {
	ID      idutil.SafeID
	Steps   []Step
	Timeout time.Duration
}

// Len returns the number of steps.
func (d *Definition) Len() int { return len(d.Steps) }

// Validate returns nil iff:
//   - ID is non-empty SafeID (use idutil.SafeID.Validate)
//   - len(Steps) >= 1
//   - each Step.Name is non-empty SafeID
//   - Step.Name values are distinct within the Definition
//   - each Step.Run is non-nil (StepFunc; Compensate may be nil)
//   - Step.Timeout >= 0
//   - Definition.Timeout >= 0
//
// Errors: errcode.KindInvalid + ErrValidationFailed, or errcode.KindConflict
// + ErrConflict for duplicate step names. Runtime data (IDs, step index) is
// carried in WithDetails or WithInternal; Message is a const literal.
func (d *Definition) Validate() error {
	if d.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: missing ID")
	}
	if err := d.ID.Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: invalid ID",
			errcode.WithDetails(slog.String("id", string(d.ID))),
		)
	}
	if len(d.Steps) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: must have at least one step",
			errcode.WithDetails(slog.String("definitionId", string(d.ID))),
		)
	}
	if d.Timeout < 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: Timeout must be >= 0",
			errcode.WithDetails(slog.String("definitionId", string(d.ID))),
		)
	}
	seen := make(map[idutil.SafeID]int, len(d.Steps))
	for i, step := range d.Steps {
		if step.Name == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"saga definition: step missing Name",
				errcode.WithDetails(
					slog.String("definitionId", string(d.ID)),
					slog.Int("stepIndex", i),
				),
			)
		}
		if step.Run == nil {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"saga definition: step Run must not be nil",
				errcode.WithDetails(
					slog.String("definitionId", string(d.ID)),
					slog.Int("stepIndex", i),
					slog.String("stepName", string(step.Name)),
				),
			)
		}
		if step.Timeout < 0 {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"saga definition: step Timeout must be >= 0",
				errcode.WithDetails(
					slog.String("definitionId", string(d.ID)),
					slog.Int("stepIndex", i),
					slog.String("stepName", string(step.Name)),
				),
			)
		}
		if prev, dup := seen[step.Name]; dup {
			return errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"saga definition: duplicate step Name",
				errcode.WithDetails(
					slog.String("definitionId", string(d.ID)),
					slog.String("stepName", string(step.Name)),
					slog.Int("firstIndex", prev),
					slog.Int("duplicateIndex", i),
				),
			)
		}
		seen[step.Name] = i
	}
	return nil
}

// Registry resolves a Definition for a claimed Instance. Coordinator looks up
// by Instance.DefinitionID. Returns ok=false when the DefinitionID isn't
// registered (Coordinator treats this as terminal-Failed).
//
// Interface (not concrete map) so PR-07's contractgen can emit typed
// SagaRegistry_<Name> values, and PR-08 archtest can check holders use the
// interface, not a closure.
type Registry interface {
	Lookup(definitionID idutil.SafeID) (*Definition, bool)
}

// InMemoryRegistry is a trivial map-backed Registry for tests and the PR-09
// example. Construction validates each Definition via Definition.Validate;
// the constructor returns an error on the first invalid definition. Lookup is
// read-only and race-safe (no mutation after construction).
type InMemoryRegistry struct {
	defs map[idutil.SafeID]*Definition
}

// NewInMemoryRegistry validates each definition then registers it by ID.
// Duplicate IDs return errcode.KindConflict + ErrConflict.
func NewInMemoryRegistry(defs ...*Definition) (*InMemoryRegistry, error) {
	r := &InMemoryRegistry{
		defs: make(map[idutil.SafeID]*Definition, len(defs)),
	}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			return nil, err
		}
		if _, exists := r.defs[d.ID]; exists {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"saga registry: duplicate definition ID",
				errcode.WithDetails(slog.String("definitionId", string(d.ID))),
			)
		}
		r.defs[d.ID] = d
	}
	return r, nil
}

// Lookup implements Registry.
func (r *InMemoryRegistry) Lookup(id idutil.SafeID) (*Definition, bool) {
	d, ok := r.defs[id]
	return d, ok
}
