package healthz

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// Probe is a named readiness check.
//
// Name returns a typed [ProbeName] (snake_case lowercase). See [ProbeName]
// for the typed-funnel discipline: the name flows through declared typed
// const (adapter / framework / cellgen) or through a composed-name
// constructor like [EmitterFailOpenProbeName]; bare strings never reach
// [NewProbe] at production callsites. Compile-time `Name() ProbeName`
// closes the form-uniqueness gap that the legacy `Name() string` left
// open (downstream Hard via type system; see archtest
// PROBENAME-SEALED-FUNNEL-01).
//
// Check is invoked by the Aggregator with a context carrying the probe
// deadline. Returning nil indicates healthy; a non-nil error indicates
// degraded or down — the Aggregator classifies the outcome by inspecting
// errors.Is(err, context.DeadlineExceeded) (timeout → Down) and the error
// chain (anything else non-nil → Down by default; cells may signal a
// fail-open degraded condition with a sentinel error matched by the
// transport layer).
//
// ref: kubernetes/kubernetes apiserver healthz.HealthChecker
// (Name + Check). GoCell drops the *http.Request dependency: probes have
// no HTTP context at the kernel layer.
type Probe interface {
	Name() ProbeName
	Check(ctx context.Context) error
}

// Prober is the check-only narrowing of [Probe], consumed by the typed
// registration entry [github.com/ghbvf/gocell/kernel/cell.Registrar.RegisterReadiness].
// Name flows in independently as a [ProbeName] parameter, so name and check
// cannot drift — the funnel makes "register probe under name X but expose
// itself as name Y" structurally impossible at the entry point.
//
// Every [Probe] is automatically a [Prober].
type Prober interface {
	Check(ctx context.Context) error
}

// ProberFunc adapts a bare check function to the [Prober] interface, for the
// common cellgen / framework registration pattern where the underlying source
// (e.g. RepoProber.RepoReady, *sql.DB.PingContext) does not already
// satisfy Prober and a closure is the simplest bridge.
type ProberFunc func(ctx context.Context) error

// Check satisfies [Prober] by invoking the underlying function.
func (f ProberFunc) Check(ctx context.Context) error { return f(ctx) }

// RepoProber is implemented by a cell's primary repository/store to expose a
// differentiated readiness check.
//
// "Differentiated" means the check exercises a failure domain distinct from
// adapter pool-level pings (e.g. postgres_ready). A RepoReady implementation
// backed by SQL is expected to issue a representative query against the
// cell's own relation(s) — surfacing schema/migration drift, missing tables,
// and table-level permission loss that a connection ping cannot detect.
// In-memory implementations return nil (always ready).
//
// RepoProber is a *semantic* narrowing distinct from [Prober] — the
// `RepoReady` method name encodes the contract (representative table
// query, not a generic ping). Cellgen-generated `RegisterReadiness` takes
// a RepoProber and wraps it into a [Probe] under the cell-derived
// canonical name "<cellid>_repo_ready"; naming, wrapping, and
// registration are all centralized in the cellgen template.
type RepoProber interface {
	RepoReady(ctx context.Context) error
}

// NewProbe constructs a Probe with the given typed name and check function.
// It is the only sanctioned way to wrap a closure as a Probe — cells and
// adapters that need a one-off probe go through this constructor.
//
// NewProbe panics if name is empty or fn is nil — both are programmer
// errors caught at composition time, not runtime failures of a healthy
// probe execution. The panics route through panicregister.Approved so the
// kernel recovery middleware classifies them correctly.
func NewProbe(name ProbeName, fn func(context.Context) error) Probe {
	if name == "" {
		panic(panicregister.Approved("healthz-probe-empty-name",
			errcode.Assertion("healthz.NewProbe: name must not be empty")))
	}
	if fn == nil {
		panic(panicregister.Approved("healthz-probe-nil-fn",
			errcode.Assertion("healthz.NewProbe: fn must not be nil")))
	}
	return funcProbe{name: name, fn: fn}
}

// ProbeSet is implemented by components that expose a collection of Probe
// values belonging to the same owner (e.g. outbox.DirectEmitter registers one
// probe per cell). Callers iterate Probes() and register each with an
// Aggregator via the shared kernel funnel cell.RegisterEmitterHealthProbes.
//
// ref: kernel/outbox.DirectEmitter.Probes — primary implementor
type ProbeSet interface {
	Probes() []Probe
}

// funcProbe is the unexported Probe implementation returned by NewProbe.
// Keeping it unexported funnels all closure-based probes through NewProbe,
// which makes "wrap a closure as a Probe" form-unique for archtest.
type funcProbe struct {
	name ProbeName
	fn   func(context.Context) error
}

func (p funcProbe) Name() ProbeName                 { return p.name }
func (p funcProbe) Check(ctx context.Context) error { return p.fn(ctx) }
