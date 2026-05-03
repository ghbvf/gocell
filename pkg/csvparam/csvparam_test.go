package csvparam

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTrimsDedupesAndSorts(t *testing.T) {
	t.Parallel()

	assert.Nil(t, Parse(""))
	assert.Nil(t, Parse(" , "))
	assert.Equal(t, []string{"Cell", "Contract"}, Parse(" Contract,Cell,Cell ,, "))
}

func TestParseAllowedRejectsWithoutEchoingToken(t *testing.T) {
	t.Parallel()

	_, err := ParseAllowed("good,secret-token", []string{"good"}, "kinds")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kinds token")
	assert.Contains(t, err.Error(), "good")
	assert.NotContains(t, err.Error(), "secret-token")
}
