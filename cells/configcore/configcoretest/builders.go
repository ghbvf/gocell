package configcoretest

import (
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

// BuildWriteOption configures BuildWriteService.
type BuildWriteOption func(*buildWriteConfig)

type buildWriteConfig struct {
	repo   *FakeConfigRepository
	clk    clock.Clock
	logger *slog.Logger
}

// WithWriteRepository injects a custom FakeConfigRepository.
// Use this when a test needs direct access to the repository for seeding or
// assertions after service operations.
func WithWriteRepository(r *FakeConfigRepository) BuildWriteOption {
	return func(c *buildWriteConfig) {
		if r != nil {
			c.repo = r
		}
	}
}

// WithWriteClock injects a custom clock into both the repository and the service.
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

// BuildWriteService constructs a configwrite.Service wired with an in-memory
// repository and an outboxtest.Recorder as the event emitter. The Recorder is
// returned alongside the service for post-operation assertions.
//
// Defaults:
//   - repo: NewFakeConfigRepository(clock.Real())
//   - tx: cell.DemoCellTxManager()
//   - emitter: outboxtest.NewRecorder()
//   - logger: slog.New(slog.DiscardHandler)
//   - clock: clock.Real()
func BuildWriteService(t *testing.T, opts ...BuildWriteOption) (*configwrite.Service, *outboxtest.Recorder) {
	t.Helper()

	cfg := &buildWriteConfig{
		clk:    clock.Real(),
		logger: slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(cfg)
	}
	// Build repo after options so clock override takes effect.
	if cfg.repo == nil {
		cfg.repo = NewFakeConfigRepository(cfg.clk)
	}

	rec := outboxtest.NewRecorder()
	svc, err := configwrite.NewService(
		cfg.repo,
		cfg.logger,
		cfg.clk,
		configwrite.WithTxManager(cell.DemoCellTxManager()),
		configwrite.WithEmitter(rec),
	)
	if err != nil {
		t.Fatalf("configcoretest.BuildWriteService: %v", err)
	}
	return svc, rec
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

	return configsubscribe.NewService(
		cfg.logger,
		configsubscribe.WithClock(cfg.clk),
	)
}
