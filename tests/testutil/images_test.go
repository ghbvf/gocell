package testutil

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// imagePinnedByDigest enforces "digest mandatory" — the @sha256:<64-hex> suffix
// is the immutable content reference and the sole pinning mechanism. The tag
// segment is informational (any printable tag is accepted) because not every
// image uses SemVer (e.g. MinIO ships ISO timestamp tags like
// RELEASE.YYYY-MM-DDTHH-MM-SSZ). Pinning strength derives from the digest, not
// the tag — equivalent to k8s ImagePullPolicy + digest pin best practice.
var imagePinnedByDigest = regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._\-]+@sha256:[a-f0-9]{64}$`)

// TestContainerImagesPinned verifies that all testcontainer image constants
// use tag+digest pinning, not floating tags like "postgres:15-alpine".
func TestContainerImagesPinned(t *testing.T) {
	images := map[string]string{
		"PostgresImage":      PostgresImage,
		"RedisImage":         RedisImage,
		"RabbitMQImage":      RabbitMQImage,
		"VaultImage":         VaultImage,
		"OTelCollectorImage": OTelCollectorImage,
		"MinIOImage":         MinIOImage,
	}
	for name, image := range images {
		t.Run(name, func(t *testing.T) {
			assert.Regexp(t, imagePinnedByDigest, image,
				"%s = %q must be tag+digest pinned (name:tag@sha256:digest)", name, image)
		})
	}
}

// TestContainerImagesPinned_RejectsFloating verifies that floating tags (no
// digest) are caught by the pinned regex. These are negative examples.
func TestContainerImagesPinned_RejectsFloating(t *testing.T) {
	floating := []string{
		"postgres:15-alpine",
		"postgres:15.13-alpine",
		"redis:7-alpine",
		"redis:7.4.2-alpine",
		"rabbitmq:3-management-alpine",
		"rabbitmq:3.12.14-management-alpine",
		"hashicorp/vault:1.17",
		"otel/opentelemetry-collector:0.123.0",
		"minio/minio:latest",
		"minio/minio:RELEASE.2024-10-13T13-34-11Z",
	}

	for _, img := range floating {
		assert.NotRegexp(t, imagePinnedByDigest, img,
			"%q lacks a digest and must NOT match the pinned pattern", img)
	}
}

// TestContainerImagesPinned_RejectsMalformedDigest verifies that malformed
// digest suffixes (wrong algorithm, short hex, non-hex chars) are rejected.
func TestContainerImagesPinned_RejectsMalformedDigest(t *testing.T) {
	malformed := []string{
		"minio/minio:latest@sha256:short",
		"minio/minio:latest@sha512:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e",
		"minio/minio:latest@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936Z",
	}
	for _, img := range malformed {
		assert.NotRegexp(t, imagePinnedByDigest, img,
			"%q has a malformed digest and must NOT match the pinned pattern", img)
	}
}
