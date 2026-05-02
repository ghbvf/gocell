package bootstrap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"

	"github.com/ghbvf/gocell/kernel/clock"
)

const lcOptDNeg1 = time.Duration(-1)

func TestWithLifecycleDefaultStartTimeout_PopulatesField(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero retains default sentinel", 0, 0},
		{"positive sets explicit", testtime.D7s, testtime.D7s},
		{"negative disables", lcOptDNeg1, lcOptDNeg1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(WithClock(clock.Real()), WithLifecycleDefaultStartTimeout(tc.in))
			assert.Equal(t, tc.want, b.defaultStartTimeout)
		})
	}
}

func TestWithLifecycleDefaultStopTimeout_PopulatesField(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero retains default sentinel", 0, 0},
		{"positive sets explicit", testtime.EventuallyDefault, testtime.EventuallyDefault},
		{"negative disables", lcOptDNeg1, lcOptDNeg1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(WithClock(clock.Real()), WithLifecycleDefaultStopTimeout(tc.in))
			assert.Equal(t, tc.want, b.defaultStopTimeout)
		})
	}
}

func TestWithLifecycleDefaultTimeouts_NotCalled_FieldsZero(t *testing.T) {
	b := New(WithClock(clock.Real()))
	assert.Equal(t, time.Duration(0), b.defaultStartTimeout,
		"unset option must leave field zero so NewLifecycle falls back to DefaultStartTimeout constant")
	assert.Equal(t, time.Duration(0), b.defaultStopTimeout,
		"unset option must leave field zero so NewLifecycle falls back to DefaultStopTimeout constant")
}
