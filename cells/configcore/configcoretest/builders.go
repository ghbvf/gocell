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

// discardLogger is a logger that silently drops all log output. It is used as
// the default logger in test builders so that service internals do not pollute
// test output. Pass a real slog.Logger via the relevant option when you need to
// inspect log output in a test.
var discardLogger = slog.New(slog.DiscardHandler)

// buildWriteConfig holds the assembled components for BuildWriteService.
type buildWriteConfig struct {
	repo  *FakeConfigRepository
	clock clock.Clock
}

// BuildWriteOption is a functional option for BuildWriteService.
type BuildWriteOption func(*buildWriteConfig)

// WithWriteRepository replaces the default FakeConfigRepository.
func WithWriteRepository(r *FakeConfigRepository) BuildWriteOption {
	return func(c *buildWriteConfig) {
		if r != nil {
			c.repo = r
		}
	}
}

// WithWriteClock replaces the default clock.Real() used by the write service.
func WithWriteClock(clk clock.Clock) BuildWriteOption {
	return func(c *buildWriteConfig) {
		if clk != nil {
			c.clock = clk
		}
	}
}

// BuildWriteService constructs a docker-free configwrite.Service wired with an
// in-memory FakeConfigRepository, cell.DemoCellTxManager, and an
// outboxtest.Recorder as the Emitter. The second return value is the Recorder
// so callers can assert which events were emitted.
//
// Default assembly:
//   - FakeConfigRepository (in-memory map)
//   - cell.DemoCellTxManager (pass-through, no real transaction; testutil/demo
//     only — not the persistence.WrapForCell composition root path)
//   - outboxtest.NewRecorder() (captures emitted entries)
//   - slog.DiscardHandler logger (silences all log output in tests)
//   - clock.Real()
//
// opts override any of the defaults above.
func BuildWriteService(t *testing.T, opts ...BuildWriteOption) (*configwrite.Service, *outboxtest.Recorder) {
	t.Helper()

	cfg := &buildWriteConfig{
		repo:  NewFakeConfigRepository(),
		clock: clock.Real(),
	}
	for _, o := range opts {
		o(cfg)
	}

	rec := outboxtest.NewRecorder()
	svc, err := configwrite.NewService(
		cfg.repo,
		discardLogger,
		cfg.clock,
		configwrite.WithEmitter(rec),
		configwrite.WithTxManager(cell.DemoCellTxManager()),
	)
	if err != nil {
		t.Fatalf("configcoretest.BuildWriteService: %v", err)
	}
	return svc, rec
}

// buildSubscribeConfig holds the assembled components for BuildSubscribeService.
type buildSubscribeConfig struct {
	clock clock.Clock
}

// BuildSubscribeOption is a functional option for BuildSubscribeService.
type BuildSubscribeOption func(*buildSubscribeConfig)

// WithSubscribeClock sets the clock for the subscribe service.
func WithSubscribeClock(clk clock.Clock) BuildSubscribeOption {
	return func(c *buildSubscribeConfig) {
		if clk != nil {
			c.clock = clk
		}
	}
}

// BuildSubscribeService constructs a docker-free configsubscribe.Service.
//
// Default assembly:
//   - clock.Real() — kernel/clock/clockmock is depguard-restricted to
//     testutil/ and storetest/ paths; configcoretest/ is not exempt, so the
//     default clock remains clock.Real(). Pass WithSubscribeClock to inject a
//     deterministic clock in tests that exercise time-dependent logic.
//   - slog.DiscardHandler logger (silences all log output in tests)
//   - NopMetrics (no-op ConfigEventCollector / EventbusCacheCollector)
//   - TombstoneTTL = defaultTombstoneTTL (idempotency.DefaultTTL)
//
// opts override any of the defaults above.
func BuildSubscribeService(t *testing.T, opts ...BuildSubscribeOption) *configsubscribe.Service {
	t.Helper()

	cfg := &buildSubscribeConfig{
		clock: clock.Real(),
	}
	for _, o := range opts {
		o(cfg)
	}

	svc := configsubscribe.NewService(
		discardLogger,
		configsubscribe.WithClock(cfg.clock),
	)
	return svc
}
