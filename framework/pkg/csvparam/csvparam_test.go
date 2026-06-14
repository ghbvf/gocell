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
	assert.Contains(t, err.Error(), "invalid kinds")
	assert.Contains(t, err.Error(), "good") // allowed list appears in message
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestParseAllowed_CollectsAllUnknownTokens(t *testing.T) {
	t.Parallel()

	_, err := ParseAllowed("a,b,c", []string{}, "kinds")
	require.Error(t, err)

	var ute UnknownTokenError
	require.ErrorAs(t, err, &ute)
	assert.Equal(t, []string{"a", "b", "c"}, ute.Tokens)
}

func TestUnknownTokenError_MessageDoesNotContainInput(t *testing.T) {
	t.Parallel()

	_, err := ParseAllowed("<script>alert(1)</script>", []string{"safe"}, "kinds")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "<script>",
		"error message must not echo user-supplied token values")
}
