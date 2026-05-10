package command

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
)

func TestSweeperLifecycle_ContributesHook(t *testing.T) {
	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q, clock.Real(),
		kcommand.WithSweeperInterval(time.Hour))
	require.NoError(t, err)
	lc := NewSweeperLifecycle("devicecommand.sweeper", sw)

	hook := lc.Hook()
	assert.Equal(t, "devicecommand.sweeper", hook.Name)
	assert.NotNil(t, hook.OnStart)
	assert.NotNil(t, hook.OnStop)
}

func TestSweeperLifecycle_StartStop(t *testing.T) {
	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q, clock.Real(),
		kcommand.WithSweeperInterval(time.Hour))
	require.NoError(t, err)
	lc := NewSweeperLifecycle("", sw)

	require.NoError(t, lc.Start(context.Background()))
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
	require.NoError(t, lc.Stop(stopCtx), "Stop must be idempotent")
}
