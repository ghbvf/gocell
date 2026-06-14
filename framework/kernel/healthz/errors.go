package healthz

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// ErrDuplicateProbe signals that Aggregator.Register received a Probe whose
// Name() collides with an already-registered probe. Maps to KindConflict /
// HTTP 409 when surfaced through Bootstrap diagnostics.
//
// Probe registration is expected to happen at cell Init; a duplicate name
// is a programmer / composition error, not a runtime failure of the probe
// itself.
var ErrDuplicateProbe = errcode.New(
	errcode.KindConflict,
	errcode.ErrConflict,
	"healthz: duplicate probe name",
)

// ErrInvalidProbeName signals that a Probe's Name() failed validation —
// empty, contains characters outside [a-z0-9_], or otherwise malformed.
// Maps to KindInvalid / HTTP 400.
//
// Implementations may surface this from Register, or from NewProbe via the
// panicregister funnel when the name is empty at construction time.
var ErrInvalidProbeName = errcode.New(
	errcode.KindInvalid,
	errcode.ErrValidationFailed,
	"healthz: invalid probe name",
)
