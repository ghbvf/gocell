package appender

import "fmt"

// ActorMode is a sealed enum selecting an actor-extraction strategy. The
// unexported field forbids zero-value or ad-hoc construction by external
// callers; only the package-level instances ActorAcceptUserFallback and
// ActorRequireExplicit are valid.
type ActorMode struct{ v uint8 }

var (
	// ActorAcceptUserFallback is the strategy for user / config / session
	// audit slices: prefer payload.actorId, fall back to payload.userId
	// when actorId is absent. Reject with PermanentError when neither is
	// present (B2-C-05 fail-closed).
	ActorAcceptUserFallback = ActorMode{v: 1}

	// ActorRequireExplicit is the strategy for the role audit slice: only
	// payload.actorId is accepted; payload.userId in role events identifies
	// the target, not the actor. Reject with PermanentError when actorId
	// is missing (B2-C-05 fail-closed).
	ActorRequireExplicit = ActorMode{v: 2}
)

// Spec is the sealed per-slice configuration consumed by NewService. The
// unexported fields force construction through MustNewSpec, which validates
// against the closed set of slice names auditcoreAppenderSliceNames.
type Spec struct {
	name string
	mode ActorMode
}

// Name returns the slice name (used as the log/error prefix).
func (s Spec) Name() string { return s.name }

// Mode returns the actor-extraction strategy.
func (s Spec) Mode() ActorMode { return s.mode }

// auditcoreAppenderSliceNames is the closed set of permitted slice names.
// Adding a new auditappend* slice requires extending both this list and
// the AUDITCORE-APPENDER-SINGLE-SOURCE-01 archtest's package list.
var auditcoreAppenderSliceNames = []string{
	"auditappenduser",
	"auditappendconfig",
	"auditappendsession",
	"auditappendrole",
}

// MustNewSpec constructs a Spec for the named auditappend slice. Panics
// (PANIC-REGISTERED-01 A-class: configuration error at init time) when:
//   - mode is the zero value (caller did not pass an ActorAcceptUserFallback
//     or ActorRequireExplicit instance)
//   - name is not in the auditcoreAppenderSliceNames whitelist
func MustNewSpec(name string, mode ActorMode) Spec {
	if mode.v == 0 {
		panic("appender.MustNewSpec: invalid ActorMode (zero value); use appender.ActorAcceptUserFallback or appender.ActorRequireExplicit")
	}
	for _, allowed := range auditcoreAppenderSliceNames {
		if name == allowed {
			return Spec{name: name, mode: mode}
		}
	}
	panic(fmt.Sprintf(
		"appender.MustNewSpec: unknown slice name %q; whitelist: %s",
		name, joinNames(auditcoreAppenderSliceNames)))
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
