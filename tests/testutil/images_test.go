package testutil

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

var imagePinnedByDigest = regexp.MustCompile(`^[a-z0-9./-]+:\d+\.\d+(\.\d+)?[a-z0-9.-]*@sha256:[a-f0-9]{64}$`)

// TestContainerImagesPinned verifies that all testcontainer image constants
// use tag+digest pinning, not floating tags like "postgres:15-alpine".
func TestContainerImagesPinned(t *testing.T) {
	images := map[string]string{
		"PostgresImage": PostgresImage,
		"RedisImage":    RedisImage,
		"RabbitMQImage": RabbitMQImage,
		"VaultImage":    VaultImage,
	}
	for name, image := range images {
		t.Run(name, func(t *testing.T) {
			assert.Regexp(t, imagePinnedByDigest, image,
				"%s = %q must be tag+digest pinned (name:version@sha256:digest)", name, image)
		})
	}
}

// TestContainerImagesPinned_RejectsFloating verifies that floating tags are
// caught by the pinned regex. These are negative examples.
func TestContainerImagesPinned_RejectsFloating(t *testing.T) {
	floating := []string{
		"postgres:15-alpine",
		"postgres:15.13-alpine",
		"redis:7-alpine",
		"redis:7.4.2-alpine",
		"rabbitmq:3-management-alpine",
		"rabbitmq:3.12.14-management-alpine",
		"hashicorp/vault:1.17",
	}

	for _, img := range floating {
		assert.NotRegexp(t, imagePinnedByDigest, img,
			"%q lacks a digest and must NOT match the pinned pattern", img)
	}
}
