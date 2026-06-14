package saga

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
)

// StepFunc executes one saga step body. ctx carries the instance's deadline;
// inst is read-only metadata (mutating it is a programmer error — the
// Coordinator owns transitions). prevState is the opaque JSON payload of the
// previous step's success event (nil for the first step). Returns new opaque
// JSON state, or a non-nil error to signal step failure.
//
// # Cooperative cancellation contract
//
// Step.Timeout / Definition.Timeout are enforced via context deadline only —
// Go has no preemptive goroutine cancellation, so a Step.Run that ignores
// ctx.Done() blocks its driver's goroutine until it returns naturally. Step
// authors MUST select on ctx.Done() inside any IO / blocking primitive.
//
// Best-effort recovery when a step ignores cancellation: the driver (the
// Coordinator's drain on Stop, or the per-step Executor once Execute returns)
// stops extending the lease; once the lease expires another coordinator (or a
// restart) re-claims the instance. There is no preemptive kill.
type StepFunc func(ctx context.Context, inst *Instance, prevState []byte) (newState []byte, err error)

// CompensateFunc undoes one previously-committed step during the Compensating
// phase. It MUST be pure-reverse: it receives the step's committed state and
// reverses that step's effect through the same domain ports the forward Run
// used, never touching the transaction/outbox layer (the Coordinator owns the
// transaction; Compensate runs in the application domain only). This purity is
// statically enforced by archtest SAGA-STEP-COMPENSATE-PURE-01 — a Compensate
// body that calls *sql.Tx / pgx.Tx / outbox.Writer / outbox.Emitter /
// persistence.TxRunner fails the build.
type CompensateFunc func(ctx context.Context, inst *Instance, committedState []byte) error

// RetryPolicy configures exponential-backoff retry for a step. It is a value
// type (not a pointer) so the zero value means "inherit": a Step's zero
// RetryPolicy inherits the Definition's, and a Definition's zero RetryPolicy
// falls back to the executor's package defaults. The runtime executor
// (runtime/saga/executor) resolves the effective policy and computes the
// backoff; this type only carries the configuration.
//
// Field semantics (note the deliberate deviation from Temporal's RetryPolicy,
// where MaxAttempts==0 means unlimited):
//   - MaxAttempts: total Run invocations including the first. 0 => inherit;
//     a resolved 1 means a single attempt with no retry. Saga has no
//     "unlimited" option — retries are bounded, then compensation runs.
//   - BaseInterval: first backoff base (the delay before the first retry).
//     0 => inherit / executor default.
//   - MaxInterval: backoff cap. 0 => inherit / executor default.
type RetryPolicy struct {
	// MaxAttempts is the total number of Run invocations (including the first).
	// NOTE: 0 means "inherit", not unlimited — saga has no unlimited-retry option.
	MaxAttempts  int
	BaseInterval time.Duration
	MaxInterval  time.Duration
}

// Validate returns nil iff MaxAttempts >= 0, both intervals are >= 0, and
// (when both are non-zero) MaxInterval >= BaseInterval. Errors are
// errcode.KindInvalid + ErrValidationFailed with a const-literal message.
func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga retry policy: MaxAttempts must be >= 0",
			errcode.WithDetails(errcode.PublicInt("maxAttempts", p.MaxAttempts)),
		)
	}
	if p.BaseInterval < 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga retry policy: BaseInterval must be >= 0",
			errcode.WithDetails(errcode.PublicDuration("baseInterval", p.BaseInterval)),
		)
	}
	if p.MaxInterval < 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga retry policy: MaxInterval must be >= 0",
			errcode.WithDetails(errcode.PublicDuration("maxInterval", p.MaxInterval)),
		)
	}
	if p.BaseInterval > 0 && p.MaxInterval > 0 && p.MaxInterval < p.BaseInterval {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga retry policy: MaxInterval must be >= BaseInterval",
			errcode.WithDetails(
				errcode.PublicDuration("baseInterval", p.BaseInterval),
				errcode.PublicDuration("maxInterval", p.MaxInterval),
			),
		)
	}
	return nil
}

// Step is a single named unit inside a Definition. Name MUST be a valid
// SafeID and flows into journal Event.StepName.
//
// Compensate is the optional pure-reverse rollback for this step; nil means
// the step has nothing to undo (forward-failure is terminal, and the reverse
// compensation walk skips it). RetryPolicy overrides Definition.RetryPolicy
// for this step; its zero value inherits (mirroring Timeout's
// "0 => inherit Definition.Timeout").
type Step struct {
	Name       idutil.SafeID
	Run        StepFunc
	Compensate CompensateFunc // optional; nil => no rollback for this step
	Timeout    time.Duration  // per-step; 0 => inherit Definition.Timeout
	// RetryPolicy overrides Definition.RetryPolicy for this step.
	// NOTE: 0 means "inherit", not unlimited — saga has no unlimited-retry option.
	RetryPolicy RetryPolicy
}

// Definition is the static recipe for a saga. ID is the DefinitionID stored
// on every Instance enrolled under this definition. Steps execute in slice
// order. Timeout is the overall ceiling (Expired); the Coordinator enforces
// total-saga timeout (see Coordinator.driveOne).
//
// RetryPolicy is the saga-wide default backoff policy; each Step inherits it
// unless the Step sets its own (zero value => inherit). Both additions are
// forward-compatible new optional fields.
type Definition struct {
	ID          idutil.SafeID
	Steps       []Step
	Timeout     time.Duration
	RetryPolicy RetryPolicy
}

// Len returns the number of steps.
func (d *Definition) Len() int { return len(d.Steps) }

// Validate returns nil iff:
//   - ID is non-empty SafeID (use idutil.SafeID.Validate)
//   - len(Steps) >= 1
//   - each Step.Name is non-empty AND passes idutil.SafeID.Validate
//   - Step.Name values are distinct within the Definition
//   - each Step.Run is non-nil (StepFunc); Step.Compensate may be nil
//   - Step.Timeout >= 0
//   - each Step.RetryPolicy and Definition.RetryPolicy pass RetryPolicy.Validate
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
			errcode.WithDetails(errcode.PublicString("id", string(d.ID))),
		)
	}
	if len(d.Steps) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: must have at least one step",
			errcode.WithDetails(errcode.PublicString("definitionId", string(d.ID))),
		)
	}
	if d.Timeout < 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: Timeout must be >= 0",
			errcode.WithDetails(errcode.PublicString("definitionId", string(d.ID))),
		)
	}
	if err := d.RetryPolicy.Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: invalid RetryPolicy",
			errcode.WithDetails(errcode.PublicString("definitionId", string(d.ID))),
			errcode.WithInternal(errcode.InternalAttr("_", err.Error())),
		)
	}
	seen := make(map[idutil.SafeID]int, len(d.Steps))
	for i, step := range d.Steps {
		if err := d.validateStep(i, step); err != nil {
			return err
		}
		if prev, dup := seen[step.Name]; dup {
			return errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"saga definition: duplicate step Name",
				errcode.WithDetails(
					errcode.PublicString("definitionId", string(d.ID)),
					errcode.PublicString("stepName", string(step.Name)),
					errcode.PublicInt("firstIndex", prev),
					errcode.PublicInt("duplicateIndex", i),
				),
			)
		}
		seen[step.Name] = i
	}
	return nil
}

// validateStep validates a single step's fields (Name / Run / Timeout /
// RetryPolicy). Duplicate-name detection stays in Validate, which owns the
// per-Definition seen map. Split out to keep Validate's cognitive complexity
// within budget.
func (d *Definition) validateStep(i int, step Step) error {
	stepDetails := func() []errcode.PublicDetail {
		return []errcode.PublicDetail{
			errcode.PublicString("definitionId", string(d.ID)),
			errcode.PublicInt("stepIndex", i),
			errcode.PublicString("stepName", string(step.Name)),
		}
	}
	if step.Name == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: step missing Name",
			errcode.WithDetails(
				errcode.PublicString("definitionId", string(d.ID)),
				errcode.PublicInt("stepIndex", i),
			),
		)
	}
	if err := step.Name.Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: invalid step Name",
			errcode.WithDetails(stepDetails()...),
			errcode.WithInternal(errcode.InternalAttr("_", err.Error())),
		)
	}
	if step.Run == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: step Run must not be nil",
			errcode.WithDetails(stepDetails()...),
		)
	}
	if step.Timeout < 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: step Timeout must be >= 0",
			errcode.WithDetails(stepDetails()...),
		)
	}
	if err := step.RetryPolicy.Validate(); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga definition: invalid step RetryPolicy",
			errcode.WithDetails(stepDetails()...),
			errcode.WithInternal(errcode.InternalAttr("_", err.Error())),
		)
	}
	return nil
}

// Resolver resolves a Definition for a claimed Instance. Coordinator looks
// up by Instance.DefinitionID. Returns ok=false when the DefinitionID isn't
// registered (Coordinator treats this as terminal-Failed).
//
// The single-method interface is named Resolver (not "Registry") to follow
// Go's "-er for single-method interfaces" convention (io.Reader, fmt.Stringer,
// etc.); the implementing struct is still InMemoryRegistry because it owns
// registration state, but the abstract role consumers depend on is "resolve a
// definition by ID".
//
// Interface (not concrete map) so PR-07's contractgen can emit typed
// SagaResolver_<Name> values, and PR-08 archtest can check holders use the
// interface, not a closure.
type Resolver interface {
	Lookup(definitionID idutil.SafeID) (*Definition, bool)
}

// InMemoryRegistry is a trivial map-backed Resolver for tests and the PR-09
// example. Construction validates each Definition via Definition.Validate;
// the constructor returns an error on the first invalid definition. Lookup is
// read-only and race-safe (no mutation after construction).
type InMemoryRegistry struct {
	defs map[idutil.SafeID]*Definition
}

// NewInMemoryRegistry validates each definition then registers it by ID.
// Nil definitions return errcode.KindInvalid + ErrValidationFailed.
// Duplicate IDs return errcode.KindConflict + ErrConflict.
func NewInMemoryRegistry(defs ...*Definition) (*InMemoryRegistry, error) {
	r := &InMemoryRegistry{
		defs: make(map[idutil.SafeID]*Definition, len(defs)),
	}
	for i, d := range defs {
		if d == nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"saga registry: nil definition",
				errcode.WithDetails(errcode.PublicInt("definitionIndex", i)),
			)
		}
		if err := d.Validate(); err != nil {
			return nil, err
		}
		if _, exists := r.defs[d.ID]; exists {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"saga registry: duplicate definition ID",
				errcode.WithDetails(errcode.PublicString("definitionId", string(d.ID))),
			)
		}
		r.defs[d.ID] = d
	}
	return r, nil
}

// Lookup implements Resolver.
func (r *InMemoryRegistry) Lookup(id idutil.SafeID) (*Definition, bool) {
	d, ok := r.defs[id]
	return d, ok
}
