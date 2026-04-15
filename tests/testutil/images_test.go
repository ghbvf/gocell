package testutil

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// patchPinned matches name:X.Y.Z (3+ version segments) — redis, rabbitmq.
var patchPinned = regexp.MustCompile(`^[a-z]+:\d+\.\d+\.\d+`)

// minorPinned matches name:X.Y (2+ version segments) — postgres uses 2-segment versions.
var minorPinned = regexp.MustCompile(`^[a-z]+:\d+\.\d+`)

// TestContainerImagesPinned verifies that all testcontainer image constants
// use pinned versions, not floating tags like "postgres:15-alpine".
//
// postgres uses 2-segment versions (15.13); redis and rabbitmq use 3-segment (7.4.2, 3.12.14).
func TestContainerImagesPinned(t *testing.T) {
	t.Run("PostgresImage pinned to minor", func(t *testing.T) {
		assert.Regexp(t, minorPinned, PostgresImage,
			"PostgresImage = %q must be pinned (MAJOR.MINOR)", PostgresImage)
	})
	t.Run("RedisImage pinned to patch", func(t *testing.T) {
		assert.Regexp(t, patchPinned, RedisImage,
			"RedisImage = %q must be pinned (MAJOR.MINOR.PATCH)", RedisImage)
	})
	t.Run("RabbitMQImage pinned to patch", func(t *testing.T) {
		assert.Regexp(t, patchPinned, RabbitMQImage,
			"RabbitMQImage = %q must be pinned (MAJOR.MINOR.PATCH)", RabbitMQImage)
	})
}

// TestContainerImagesPinned_RejectsFloating verifies that floating tags are
// caught by the pinned regex. These are negative examples.
func TestContainerImagesPinned_RejectsFloating(t *testing.T) {
	floating := []string{
		"postgres:15-alpine",
		"redis:7-alpine",
		"rabbitmq:3-management-alpine",
	}

	for _, img := range floating {
		assert.NotRegexp(t, patchPinned, img,
			"%q is a floating tag and must NOT match the pinned pattern", img)
	}

	// postgres:15-alpine must also fail the minor pattern (only 1 segment before hyphen).
	assert.NotRegexp(t, minorPinned, "postgres:15-alpine",
		"postgres:15-alpine must NOT match minor-pinned pattern")
}
