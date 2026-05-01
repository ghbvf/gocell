package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
)

type queueRegistrarCell struct {
	*cell.BaseCell
	got kcommand.Queue
}

func (c *queueRegistrarCell) RegisterCommandQueue(q kcommand.Queue) {
	c.got = q
}

func TestDiscoverQueueRegistrars(t *testing.T) {
	q := commandtest.NewInMemQueue()
	registrar := &queueRegistrarCell{BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: "withqueue"})}
	plain := cell.NewBaseCell(cell.CellMetadata{ID: "plain"})

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
