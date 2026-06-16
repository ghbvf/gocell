package certlifecycle

// State is the sealed certificate-lifecycle state — the closed vocabulary of a
// device certificate's life: requested → issued → active → near-expiry →
// renewing → {rotated | revoked | expired}. It is a struct with a single
// unexported field, so a populated composite literal outside this package is a
// compile error (type-system Hard closed value-set): the only way to obtain a
// State is one of the accessor functions below or ParseState. This mirrors the
// reconcile.Tenancy / certsigning newtype convention — accessor funcs (not
// exported vars) keep the values immutable to importers.
//
// # The eight states are the lifecycle VOCABULARY, not all reconciler-driven
//
// The full eight-state machine is the issue-specified lifecycle model. PR-7's
// Reconciler behaviorally distinguishes only three classes:
//
//   - active   — the steady state the Reconciler renews (and maintains: a
//     successful renewal writes the new generation back as active). renewable.
//   - revoked  — an operator/PDP terminal the Reconciler OBSERVES and skips: a
//     revoked certificate is never renewed. NOT renewable.
//   - everything else (requested, issued, near-expiry, renewing, rotated,
//     expired) — NOT acted on by PR-7's renewal sweep:
//     · requested / issued are the EST-enroll pre-active transitions (PR-8),
//     set externally; the Reconciler skips a cert not yet active.
//     · near-expiry / renewing are DERIVED / in-flight — the Reconciler
//     computes near-expiry from (notAfter, jitter) per tick rather than
//     persisting it, and renewing is a transient in-memory phase of one
//     Reconcile; neither is written back to the row.
//     · rotated / expired are event/observation vocabulary, not row states the
//     Reconciler persists (a renewed cert is written back as active, and an
//     expired-but-recoverable cert is re-signed rather than marked dead).
//
// So the persisted-row states PR-7 reads/writes are active (maintained) and
// revoked (observed); the remaining values exist so future PRs (enroll,
// revocation) populate the same closed vocabulary.
type State struct{ v string }

// The closed value-set. Each accessor returns the canonical State; there is no
// exported variable to reassign and no exported field to set.
func StateRequested() State  { return State{v: stateRequested} }
func StateIssued() State     { return State{v: stateIssued} }
func StateActive() State     { return State{v: stateActive} }
func StateNearExpiry() State { return State{v: stateNearExpiry} }
func StateRenewing() State   { return State{v: stateRenewing} }
func StateRotated() State    { return State{v: stateRotated} }
func StateRevoked() State    { return State{v: stateRevoked} }
func StateExpired() State    { return State{v: stateExpired} }

// stateValue literals — the single source for the wire/DB string of each state
// (snake-free dotted-free lower kebab, matching the lifecycle vocabulary names).
const (
	stateRequested  = "requested"
	stateIssued     = "issued"
	stateActive     = "active"
	stateNearExpiry = "near-expiry"
	stateRenewing   = "renewing"
	stateRotated    = "rotated"
	stateRevoked    = "revoked"
	stateExpired    = "expired"
)

// String returns the canonical lifecycle string (empty for the zero value).
func (s State) String() string { return s.v }

// IsZero reports whether s is the invalid zero value (no accessor / ParseState
// produced it).
func (s State) IsZero() bool { return s.v == "" }

// renewable reports whether the Reconciler should attempt to renew a certificate
// in this state. Only active certificates are renewed: revoked is a terminal the
// Reconciler observes-and-skips, and the pre-active (requested/issued) and
// derived/terminal (near-expiry/renewing/rotated/expired) states are out of the
// PR-7 renewal sweep's scope. This is the single behavioral gate the Reconciler
// consults so a revoked/pre-active cert is never re-signed.
func (s State) renewable() bool { return s.v == stateActive }

// ParseState resolves a persisted-row string to its State, reporting ok=false
// for any value outside the closed set (fail-closed hydration — an unknown row
// state never silently becomes a valid State).
func ParseState(s string) (State, bool) {
	switch s {
	case stateRequested, stateIssued, stateActive, stateNearExpiry,
		stateRenewing, stateRotated, stateRevoked, stateExpired:
		return State{v: s}, true
	default:
		return State{}, false
	}
}

// AllStates returns the closed value-set in lifecycle order. It exists for
// table-driven tests and completeness checks (a new state added to one place but
// not here fails the AllStates length assertion).
func AllStates() []State {
	return []State{
		StateRequested(), StateIssued(), StateActive(), StateNearExpiry(),
		StateRenewing(), StateRotated(), StateRevoked(), StateExpired(),
	}
}
