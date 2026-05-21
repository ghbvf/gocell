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
//   - cell.DemoCellTxManager (pass-through, no real transaction)
//   - outboxtest.NewRecorder() (captures emitted entries)
//   - slog.Default() logger (discards in test output by default)
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
		slog.Default(),
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
//   - clock.Real()
//   - slog.Default() logger
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
		slog.Default(),
		configsubscribe.WithClock(cfg.clock),
	)
	return svc
}
