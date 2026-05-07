package command

import "github.com/ghbvf/gocell/kernel/metautil"

// Metadata size limits (META-SIZE-01) live in kernel/metautil so kernel
// transports (outbox + command) share a single source of truth. The values
// (key count, key length, value length, total size) are accessed through
// metautil.Max* constants. METADATA-LIMITS-SINGLE-SOURCE-01 archtest
// rejects any reintroduction of those constants in this package.

// validateMetadata delegates to the shared metautil limit checker with the
// "command" domain prefix so error Messages stay traceable.
func validateMetadata(m map[string]string) error {
	return metautil.ValidateLimits(m, metautil.DomainCommand)
}
