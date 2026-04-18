package rabbitmq

import (
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// safeDelay is an alias for outbox.ExponentialDelay retained so existing
// rabbitmq tests can reference the helper without churn.
func safeDelay(base, maxDelay time.Duration, attempt int) time.Duration {
	return outbox.ExponentialDelay(base, maxDelay, attempt)
}
