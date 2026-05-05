//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func newIntegrationTestConsumerBase(t testing.TB, clk clock.Clock) *outbox.ConsumerBase {
	t.Helper()
	return newIntegrationTestConsumerBaseWithClaimer(t, idempotency.NewInMemClaimer(clk), clk)
}

func newIntegrationTestConsumerBaseWithClaimer(
	t testing.TB, claimer idempotency.Claimer, clk clock.Clock,
) *outbox.ConsumerBase {
	t.Helper()

	cb, err := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{}, clk)
	require.NoError(t, err)
	return cb
}
