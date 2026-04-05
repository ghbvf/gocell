package configcore

import (
	"context"
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCell() *ConfigCore {
	return NewConfigCore(
		WithConfigRepository(mem.NewConfigRepository()),
		WithFlagRepository(mem.NewFlagRepository()),
		WithPublisher(eventbus.New()),
	)
}

func TestConfigCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells:     make(map[string]cell.Cell),
		Contracts: make(map[string]cell.Contract),
		Config:    make(map[string]any),
	}

	// Init
	require.NoError(t, c.Init(ctx, deps))
	assert.Equal(t, 5, len(c.OwnedSlices()), "should have 5 slices")

	// Start
	require.NoError(t, c.Start(ctx))
	assert.Equal(t, "healthy", c.Health().Status)
	assert.True(t, c.Ready())

	// Stop
	require.NoError(t, c.Stop(ctx))
	assert.Equal(t, "unhealthy", c.Health().Status)
	assert.False(t, c.Ready())
}

func TestConfigCore_Metadata(t *testing.T) {
	c := newTestCell()

	assert.Equal(t, "config-core", c.ID())
	assert.Equal(t, cell.CellTypeCore, c.Type())
	assert.Equal(t, cell.L2, c.ConsistencyLevel())
	assert.Equal(t, "platform", c.Metadata().Owner.Team)
}

func TestConfigCore_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	mux := &stubMux{}
	c.RegisterRoutes(mux)
	assert.GreaterOrEqual(t, mux.handleCount, 2, "should register at least 2 route patterns")
}

func TestConfigCore_RegisterSubscriptions(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	eb := eventbus.New()
	// Should not panic.
	c.RegisterSubscriptions(eb)
	_ = eb.Close()
}

// stubMux implements cell.RouteMux for testing.
type stubMux struct {
	handleCount int
}

func (m *stubMux) Handle(_ string, _ http.Handler) {
	m.handleCount++
}

func (m *stubMux) Group(_ func(cell.RouteMux)) {
	m.handleCount++
}
