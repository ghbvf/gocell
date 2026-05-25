package distlock

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errEntropyFailedForTest = errors.New("distlock_test: entropy stub failure")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errEntropyFailedForTest }

func TestRandomToken_RandFailure(t *testing.T) {
	tok, err := randomTokenFrom(failingReader{})
	require.Error(t, err)
	assert.Empty(t, tok)
	assert.ErrorIs(t, err, errEntropyFailedForTest)
	assert.ErrorContains(t, err, "distlock: read entropy")
}
