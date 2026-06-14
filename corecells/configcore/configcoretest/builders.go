package configcoretest

import (
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/outbox"

	"github.com/ghbvf/gocell/corecells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/corecells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
)

// BuildWriteOption configures BuildWriteService.
type BuildWriteOption func(*buildWriteConfig)

type buildWriteConfig struct {
	clk    clock.Clock
	logger *slog.Logger
}

// WithWriteClock injects a custom clock. The same clock instance is wired into
// both the FakeConfigRepository and the configwrite.Service, so Create-stamped
// CreatedAt/UpdatedAt and Update-stamped UpdatedAt come from a single source.
// Use clockmock.New(t) for time-sensitive assertions.
func WithWriteClock(clk clock.Clock) BuildWriteOption {
	return func(c *buildWriteConfig) {
		if clk != nil {
			c.clk = clk
		}
	}
}

// WithWriteLogger injects a custom logger. Defaults to slog.DiscardHandler.
func WithWriteLogger(l *slog.Logger) BuildWriteOption {
	return func(c *buildWriteConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// BuildWriteService constructs a configwrite.Service wired with a fresh
// in-memory repository and an outboxtest.Recorder as the event emitter. Both
// the repository and the Recorder are returned so the test can Seed/Snapshot
// state and assert on captured outbox entries.
//
// The repository is always constructed internally with the configured clock —
// callers cannot inject a pre-built repository, which removes the clock-fork
// foot-gun where the repo's clock (Update path) would diverge from the
// service's clock (Create path).
//
// Defaults:
//   - tx: outbox.DemoCellTxManager()
//   - emitter: outboxtest.NewRecorder()
//   - logger: slog.New(slog.DiscardHandler)
//   - clock: clock.Real()
func BuildWriteService(t *testing.T, opts ...BuildWriteOption) (*configwrite.Service, *FakeConfigRepository, *outboxtest.Recorder) {
	t.Helper()

	cfg := &buildWriteConfig{
		clk:    clock.Real(),
		logger: slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(cfg)
	}
	repo := NewFakeConfigRepository(cfg.clk)

	rec := outboxtest.NewRecorder()
	svc, err := configwrite.NewService(
		cfg.clk,
		repo,
		cfg.logger,
		configwrite.WithTxManager(outbox.DemoCellTxManager()),
		configwrite.WithEmitter(rec.CellEmitter()),
	)
	if err != nil {
		t.Fatalf("configcoretest.BuildWriteService: %v", err)
	}
	return svc, repo, rec
}

// BuildSubscribeOption configures BuildSubscribeService.
type BuildSubscribeOption func(*buildSubscribeConfig)

type buildSubscribeConfig struct {
	clk    clock.Clock
	logger *slog.Logger
}

// WithSubscribeClock injects a custom clock.
// Required by configsubscribe.NewService; defaults to clock.Real().
func WithSubscribeClock(clk clock.Clock) BuildSubscribeOption {
	return func(c *buildSubscribeConfig) {
		if clk != nil {
			c.clk = clk
		}
	}
}

// WithSubscribeLogger injects a custom logger. Defaults to slog.DiscardHandler.
func WithSubscribeLogger(l *slog.Logger) BuildSubscribeOption {
	return func(c *buildSubscribeConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// BuildSubscribeService constructs a configsubscribe.Service wired with default
// settings suitable for unit tests.
//
// No Recorder is returned because configsubscribe is a pure event consumer: it
// drives a local version-tracking cache but never emits outbox events of its
// own. Use svc.Cache() to assert cache state after calling HandleEntryUpserted
// or HandleEntryDeleted.
//
// Defaults:
//   - logger: slog.New(slog.DiscardHandler)
//   - clock: clock.Real()
func BuildSubscribeService(t *testing.T, opts ...BuildSubscribeOption) *configsubscribe.Service {
	t.Helper()

	cfg := &buildSubscribeConfig{
		clk:    clock.Real(),
		logger: slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(cfg)
	}

	svc, err := configsubscribe.NewService(
		cfg.clk,
		cfg.logger,
	)
	if err != nil {
		t.Fatalf("BuildSubscribeService: %v", err)
	}
	return svc
}
