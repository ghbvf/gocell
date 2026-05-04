package gocell_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gocell "github.com/ghbvf/gocell"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

func TestPhase0Gate(t *testing.T) {
	app := gocell.NewAssembly("test-bundle")
	myCell := cell.NewBaseCell(&metadata.CellMeta{
		ID:               "test-cell",
		Type:             "core",
		ConsistencyLevel: "L1",
	})
	require.NoError(t, app.Register(myCell))
	require.NoError(t, app.Start(context.Background()))
	health := app.Health()
	require.Equal(t, "healthy", health["test-cell"].Status)
	require.NoError(t, app.Stop(context.Background()))
}
