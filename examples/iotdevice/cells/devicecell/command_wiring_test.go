package devicecell

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/outbox"
	commandruntime "github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// newWiredCommandCell builds a fully-wired devicecell over the given registry +
// device repo and drives its real Init — so the cell's production
// cmdenqueue.Register call site runs. Returns nothing extra; the caller already
// holds reg and repo.
func newWiredCommandCell(t *testing.T, reg *commandruntime.Registry, repo domain.DeviceRepository) {
	t.Helper()
	clk := clock.Real()
	c := NewDeviceCell(
		clk,
		WithDeviceRepository(repo),
		WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clk))),
		WithCommandRegistry(reg),
	)
	c.RegisterCommandQueue(commandtest.NewInMemQueue())
	require.NoError(t, c.Init(context.Background(), newTestRec()))
}

// TestDeviceCell_CommandBus_Enqueue_RealHandlerEndToEnd is the #1580 end-to-end
// proof that the generated command funnel is wired to a REAL handler through the
// cell — not dead-but-compiles. It drives the cell's actual Init (which calls
// cmdenqueue.Register via EnqueueCommandAdapter), then dispatches through the
// same registry and asserts the real devicecmd.Service.Enqueue persisted an
// entry. Deleting the cell's Register call makes Dispatch return
// ErrCommandNotFound here (the #1580 layer-3 runtime backstop).
func TestDeviceCell_CommandBus_Enqueue_RealHandlerEndToEnd(t *testing.T) {
	ctx := context.Background()

	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{ID: "dev-1", Name: "sensor", Status: "online"}))

	reg := commandruntime.NewRegistry()
	newWiredCommandCell(t, reg, repo)

	// Dispatch through the SAME registry the cell registered into. A real handler
	// must be present because the cell's Register call ran during Init.
	resp, err := cmdenqueue.Dispatch(ctx, reg, &cmdenqueue.Request{
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     "now",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Data)
	assert.Equal(t, "dev-1", resp.Data.DeviceID)
	assert.Equal(t, "reboot", resp.Data.CommandType)
	assert.Equal(t, "now", resp.Data.Payload)
	assert.NotEmpty(t, resp.Data.ID, "real Service.Enqueue must assign a command id")
}

// TestDeviceCell_CommandBus_Enqueue_UnknownDevice asserts the real handler
// propagates the domain error (device lookup failure) through Dispatch, proving
// the dispatch path reaches genuine Service logic rather than a stub.
func TestDeviceCell_CommandBus_Enqueue_UnknownDevice(t *testing.T) {
	ctx := context.Background()

	reg := commandruntime.NewRegistry()
	newWiredCommandCell(t, reg, mem.NewDeviceRepository())

	_, err := cmdenqueue.Dispatch(ctx, reg, &cmdenqueue.Request{
		DeviceID:    "ghost",
		CommandType: "reboot",
		Payload:     "now",
	})
	require.Error(t, err, "enqueue to a nonexistent device must fail through the real Service")
}
