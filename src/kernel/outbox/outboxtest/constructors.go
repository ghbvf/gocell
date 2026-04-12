package outboxtest

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// PubSubConstructor creates a fresh Publisher+Subscriber pair for a single test.
// Each test gets its own pair to ensure isolation.
// Implementations should register cleanup via t.Cleanup.
type PubSubConstructor func(t *testing.T) (outbox.Publisher, outbox.Subscriber)
