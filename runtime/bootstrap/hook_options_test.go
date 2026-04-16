package bootstrap

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
)

type recordingHookObserver struct {
	events []cell.HookEvent
}

func (r *recordingHookObserver) OnHookEvent(e cell.HookEvent) {
	r.events = append(r.events, e)
}

func TestWithHookTimeout_PopulatesField(t *testing.T) {
	b := New(WithHookTimeout(2 * time.Second))
	assert.Equal(t, 2*time.Second, b.hookTimeout)
	assert.True(t, b.hookTimeoutSet)
}

func TestWithHookTimeout_NegativeDisables(t *testing.T) {
	b := New(WithHookTimeout(-1))
	assert.Equal(t, time.Duration(-1), b.hookTimeout)
	assert.True(t, b.hookTimeoutSet)
}

func TestWithHookTimeout_NotCalled_FieldUnset(t *testing.T) {
	b := New()
	assert.False(t, b.hookTimeoutSet)
}

func TestWithHookObserver_PopulatesField(t *testing.T) {
	obs := &recordingHookObserver{}
	b := New(WithHookObserver(obs))
	assert.Same(t, obs, b.hookObserver)
}

func TestWithHookObserver_Nil_NoOp(t *testing.T) {
	b := New(WithHookObserver(nil))
	assert.Nil(t, b.hookObserver)
}
