package command

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
)

// errSweeperMock is a SweeperRunner mock that returns an error from Start
// immediately, exercising the startup-fail probe in SweeperLifecycle.Start.
type errSweeperMock struct {
	startErr error
}

func (m *errSweeperMock) Start(_ context.Context) error { return m.startErr }
func (m *errSweeperMock) Stop(_ context.Context) error  { return nil }

// TestSweeperLifecycle_StartFailImmediately verifies A2: when the underlying
// sweeper's Start returns an error immediately, lifecycle.Start must propagate
// that error to the caller (rather than silently swallowing it in a goroutine).
//
// The startup probe window (startProbeTimeout) gives the goroutine a brief
// opportunity to surface immediate failures — matching the relay.go readyCh
// pattern.
func TestSweeperLifecycle_StartFailImmediately(t *testing.T) {
	wantErr := errors.New("sweeper-start-failed")
	mock := &errSweeperMock{startErr: wantErr}
	lc := &SweeperLifecycle{Name: "test.sweeper", Sweeper: mock, Clock: clock.Real()}

	err := lc.Start(context.Background())
	require.Error(t, err, "Start must return error when sweeper Start fails immediately")
	assert.ErrorIs(t, err, wantErr, "returned error must wrap the sweeper's start error")
}

func TestSweeperLifecycle_ContributesHook(t *testing.T) {
	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q, clock.Real(),
		kcommand.WithSweeperInterval(time.Hour))
	require.NoError(t, err)
	lc := NewSweeperLifecycle("devicecommand.sweeper", sw, clock.Real())

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
	lc := NewSweeperLifecycle("", sw, clock.Real())

	require.NoError(t, lc.Start(context.Background()))
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lc.Stop(stopCtx))
	require.NoError(t, lc.Stop(stopCtx), "Stop must be idempotent")
}
