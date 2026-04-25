package command

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSweeperLifecycle_ContributesHook(t *testing.T) {
	q := commandtest.NewInMemQueue()
	lc := NewSweeperLifecycle("devicecommand.sweeper", &kcommand.Sweeper{
		Scanner:  q,
		Queue:    q,
		Interval: time.Hour,
	})

	var _ cell.LifecycleContributor = lc
	hooks := lc.LifecycleHooks()
	require.Len(t, hooks, 1)
	assert.Equal(t, "devicecommand.sweeper", hooks[0].Name)
	assert.NotNil(t, hooks[0].OnStart)
	assert.NotNil(t, hooks[0].OnStop)
}

func TestSweeperLifecycle_StartStop(t *testing.T) {
	q := commandtest.NewInMemQueue()
	lc := NewSweeperLifecycle("", &kcommand.Sweeper{
		Scanner:  q,
		Queue:    q,
		Interval: time.Hour,
	})

	require.NoError(t, lc.Start(context.Background()))
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
	require.NoError(t, lc.Stop(stopCtx), "Stop must be idempotent")
}
