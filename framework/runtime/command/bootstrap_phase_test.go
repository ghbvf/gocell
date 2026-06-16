package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	kcommand "github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/kernel/command/commandtest"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

type queueRegistrarCell struct {
	*cell.BaseCell
	got kcommand.QueueWithScanner
}

func (c *queueRegistrarCell) RegisterCommandQueue(q kcommand.QueueWithScanner) {
	c.got = q
}

func TestDiscoverQueueRegistrars(t *testing.T) {
	q := commandtest.NewInMemQueue()
	registrar := &queueRegistrarCell{BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "withqueue"})}
	plain := cell.MustNewBaseCell(&metadata.CellMeta{ID: "plain"})

	count, err := DiscoverQueueRegistrars([]cell.Cell{registrar, plain}, q)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Same(t, q, registrar.got)
}

func TestDiscoverQueueRegistrars_NilQueue(t *testing.T) {
	count, err := DiscoverQueueRegistrars(nil, nil)
	require.Error(t, err)
	assert.Zero(t, count)
}

func TestDiscoverQueueRegistrarsInAssembly_NilAssembly(t *testing.T) {
	// A nil assembly is a wiring error, not an empty-cell no-op: fail fast with a
	// non-nil queue so the guard (not the queue==nil path) is what trips.
	count, err := DiscoverQueueRegistrarsInAssembly(nil, commandtest.NewInMemQueue())
	require.Error(t, err)
	assert.Zero(t, count)
}
