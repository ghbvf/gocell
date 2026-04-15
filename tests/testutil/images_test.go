package testutil

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestContainerImagesPinned verifies that all testcontainer image constants
// use pinned patch versions (e.g., "postgres:15.13-alpine"), not floating
// minor tags (e.g., "postgres:15-alpine").
func TestContainerImagesPinned(t *testing.T) {
	// Pattern: name:MAJOR.MINOR[.PATCH][-suffix]
	// Accepts: postgres:15.13-alpine, redis:7.4.2-alpine, rabbitmq:3.12.14-management-alpine
	// Rejects: postgres:15-alpine, redis:7-alpine (floating minor — only major before first hyphen)
	pinned := regexp.MustCompile(`^[a-z]+:\d+\.\d+`)

	images := map[string]string{
		"PostgresImage": PostgresImage,
		"RedisImage":    RedisImage,
		"RabbitMQImage": RabbitMQImage,
	}

	for name, img := range images {
		assert.Regexp(t, pinned, img,
			"%s = %q should be pinned to a patch version (e.g., X.Y.Z-suffix)", name, img)
	}
}
