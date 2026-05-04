//go:build !linux && !darwin && !windows

package initialadmin

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestPlatformSupported_ReturnsErrcode(t *testing.T) {
	err := PlatformSupported()
	require.Error(t, err, "non-linux non-darwin non-windows build must report platform unsupported")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error; got %T", err)
	assert.Equal(t, errcode.ErrCellPlatformUnsupported, ec.Code)
}

func TestPlatformSupported_MessageMentionsRemoveOption(t *testing.T) {
	err := PlatformSupported()
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "WithInitialAdminBootstrap"),
		"error message must point operators at the option to drop; got: %s", err.Error())
}
