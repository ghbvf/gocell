package bootstrap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReloadGate_BeginShutdownDrainsInflight(t *testing.T) {
	gate := newReloadGate()
	require.True(t, gate.TryEnter())

	drained := gate.BeginShutdown()

	select {
	case <-drained:
		t.Fatal("drained channel closed before the in-flight reload left")
	default:
	}

	assert.False(t, gate.TryEnter(), "shutdown must reject new reload callbacks")

	gate.Leave()

	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drained channel did not close after the in-flight reload completed")
	}
}

func TestReloadGate_BeginShutdownWithoutInflightIsImmediate(t *testing.T) {
	gate := newReloadGate()

	select {
	case <-gate.BeginShutdown():
	case <-time.After(time.Second):
		t.Fatal("shutdown without in-flight reloads should drain immediately")
	}
}

func TestReloadGate_BeginShutdownIsIdempotent(t *testing.T) {
	gate := newReloadGate()
	first := gate.BeginShutdown()
	second := gate.BeginShutdown()

	assert.True(t, first == second)
	assert.False(t, gate.TryEnter())
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("drained channel should remain closed after repeated shutdown calls")
	}
}
