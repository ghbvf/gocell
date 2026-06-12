package cellvocab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/cellvocab"
)

func TestProjectionSourceValues(t *testing.T) {
	assert.Equal(t, cellvocab.ProjectionSource("outbox"), cellvocab.ProjectionSourceOutbox)
	assert.Equal(t, cellvocab.ProjectionSource("saga-journal"), cellvocab.ProjectionSourceSagaJournal)
}

func TestAllProjectionSources(t *testing.T) {
	got := cellvocab.AllProjectionSources()
	assert.Equal(t, []cellvocab.ProjectionSource{
		cellvocab.ProjectionSourceOutbox,
		cellvocab.ProjectionSourceSagaJournal,
	}, got)

	// Returns a copy: mutating the result must not corrupt the canonical set.
	got[0] = "tampered"
	assert.Equal(t, cellvocab.ProjectionSourceOutbox, cellvocab.AllProjectionSources()[0])
}
