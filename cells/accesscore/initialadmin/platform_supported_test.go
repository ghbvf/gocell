//go:build unix || windows

package initialadmin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlatformSupported_ReturnsNil(t *testing.T) {
	assert.NoError(t, PlatformSupported(),
		"unix/windows build must report platform supported (nil error)")
}
