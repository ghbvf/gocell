package distlock

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errRandomTokenEntropyFailedForTest = errors.New("distlock_test: random-token entropy stub failure")

type failingRandomTokenReader struct{}

func (failingRandomTokenReader) Read([]byte) (int, error) {
	return 0, errRandomTokenEntropyFailedForTest
}

func TestRandomToken_RandFailure(t *testing.T) {
	tok, err := newRandomToken(failingRandomTokenReader{})
	require.Error(t, err)
	assert.Empty(t, tok)
	assert.ErrorIs(t, err, errRandomTokenEntropyFailedForTest)
	assert.ErrorContains(t, err, "distlock: read entropy")
}
