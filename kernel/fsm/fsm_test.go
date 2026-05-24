package fsm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type state int

const (
	stateA state = iota + 1
	stateB
	stateC // terminal: absent from the table below
)

// table: A → {B, C}; B → {C}; C is terminal (no entry).
var table = map[state][]state{
	stateA: {stateB, stateC},
	stateB: {stateC},
}

func TestCanTransition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		from, to state
		want     bool
	}{
		{"A->B allowed", stateA, stateB, true},
		{"A->C allowed", stateA, stateC, true},
		{"B->C allowed", stateB, stateC, true},
		{"A->A denied", stateA, stateA, false},
		{"B->A denied", stateB, stateA, false},
		{"terminal C->A denied", stateC, stateA, false},
		{"unknown state denied", state(99), stateA, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, CanTransition(table, tt.from, tt.to))
		})
	}
}

func TestAllowedTargets(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []state{stateB, stateC}, AllowedTargets(table, stateA))
	assert.Equal(t, []state{stateC}, AllowedTargets(table, stateB))
	assert.Nil(t, AllowedTargets(table, stateC), "terminal state returns nil")
	assert.Nil(t, AllowedTargets(table, state(99)), "unknown state returns nil")
}

func TestAllowedTargets_DefensiveCopy(t *testing.T) {
	t.Parallel()
	got := AllowedTargets(table, stateA)
	require.Len(t, got, 2)
	got[0] = stateC // mutate the caller's copy
	assert.Equal(t, []state{stateB, stateC}, AllowedTargets(table, stateA),
		"mutating a returned slice must not corrupt the table")
}
