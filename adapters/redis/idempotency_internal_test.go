package redis

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errClaimTokenEntropyFailedForTest = errors.New("redis_test: claim-token entropy stub failure")

type failingClaimTokenReader struct{}

func (failingClaimTokenReader) Read([]byte) (int, error) {
	return 0, errClaimTokenEntropyFailedForTest
}

func TestClaimToken_RandFailure(t *testing.T) {
	tok, err := newClaimToken(failingClaimTokenReader{})
	require.Error(t, err)
	assert.Empty(t, tok)
	assert.ErrorIs(t, err, errClaimTokenEntropyFailedForTest)
	assert.ErrorContains(t, err, "redis: read entropy")
}
