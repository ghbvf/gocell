package bootstrap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWithLifecycleDefaultStartTimeout_PopulatesField(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero retains default sentinel", 0, 0},
		{"positive sets explicit", 7 * time.Second, 7 * time.Second},
		{"negative disables", -1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(WithLifecycleDefaultStartTimeout(tc.in))
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
		{"positive sets explicit", 3 * time.Second, 3 * time.Second},
		{"negative disables", -1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(WithLifecycleDefaultStopTimeout(tc.in))
			assert.Equal(t, tc.want, b.defaultStopTimeout)
		})
	}
}

func TestWithLifecycleDefaultTimeouts_NotCalled_FieldsZero(t *testing.T) {
	b := New()
	assert.Equal(t, time.Duration(0), b.defaultStartTimeout,
		"unset option must leave field zero so NewLifecycle falls back to DefaultStartTimeout constant")
	assert.Equal(t, time.Duration(0), b.defaultStopTimeout,
		"unset option must leave field zero so NewLifecycle falls back to DefaultStopTimeout constant")
}
