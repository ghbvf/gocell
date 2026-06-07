package tailer

import (
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

const (
	defaultPollInterval = time.Second
	defaultLeaseTTL     = 30 * time.Second
	// defaultObserverCallDeadline bounds each Observer method invocation so a
	// blocking observer cannot pin the held per-projection distlock during a drain
	// (F3). Matches runtime/saga/executor.DefaultObserverCallDeadline (5s).
	defaultObserverCallDeadline = 5 * time.Second
)

// Config tunes the Tailer poll cadence and distlock lease. The Tailer does not
// expose a batch size — pagination of the (checkpoint, Head] range is owned by
// the ReplaySource (sagaprojection.SagaJournalSource pages via LoadSince).
type Config struct {
	// PollInterval is how often the tail loop attempts a leader-gated drain.
	// Default 1s.
	PollInterval time.Duration
	// LeaseTTL is the per-projection distlock TTL held during a drain. It is
	// auto-renewed by distlock's shared manager while the drain runs. Default 30s;
	// must be ≥ distlock.MinTTL (validated in NewTailer).
	LeaseTTL time.Duration
}

// DefaultConfig returns a Config with documented defaults.
func DefaultConfig() Config {
	return Config{PollInterval: defaultPollInterval, LeaseTTL: defaultLeaseTTL}
}

// Validate returns nil iff both durations are positive. Note that a Config that
// passes Validate may still be rejected by NewTailer if LeaseTTL <
// distlock.MinTTL — that additional check lives in NewTailer where distlock is
// in scope, and is separate from the positive-duration check here.
func (c Config) Validate() error {
	if c.PollInterval <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"tailer: Config.PollInterval must be positive")
	}
	if c.LeaseTTL <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"tailer: Config.LeaseTTL must be positive")
	}
	return nil
}

// Option configures a Tailer at construction.
type Option func(*Tailer)

// WithConfig replaces the entire Config (validated in NewTailer).
func WithConfig(cfg Config) Option {
	return func(t *Tailer) { t.cfg = cfg }
}

// WithLogger sets the slog.Logger. Builder-noop: a nil logger is ignored so the
// default (slog.Default()) is retained.
func WithLogger(l *slog.Logger) Option {
	return func(t *Tailer) {
		if l != nil {
			t.logger = l
		}
	}
}

// WithObserver sets the observability sink. Builder-noop: a nil (bare or typed)
// observer is ignored so the default NopObserver is retained.
func WithObserver(o Observer) Option {
	return func(t *Tailer) {
		if validation.IsNilInterface(o) {
			return
		}
		t.observer = o
	}
}
