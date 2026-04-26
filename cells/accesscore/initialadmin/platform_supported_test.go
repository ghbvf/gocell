//go:build linux || darwin || windows

package initialadmin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlatformSupported_ReturnsNil(t *testing.T) {
	assert.NoError(t, PlatformSupported(),
		"linux/darwin/windows build must report platform supported (nil error)")
}
