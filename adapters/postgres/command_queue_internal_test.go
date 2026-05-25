package postgres

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errCommandQueueEntropyFailedForTest = errors.New("postgres_test: command-queue entropy stub failure")

type failingCommandQueueReader struct{}

func (failingCommandQueueReader) Read([]byte) (int, error) {
	return 0, errCommandQueueEntropyFailedForTest
}

func TestCommandQueueNewID_RandFailure(t *testing.T) {
	id, err := newCommandQueueID(failingCommandQueueReader{})
	require.Error(t, err)
	assert.Empty(t, id)
	assert.ErrorIs(t, err, errCommandQueueEntropyFailedForTest)
	assert.ErrorContains(t, err, "command_queue: read entropy")
}
