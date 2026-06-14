//go:build integration

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

func newCorebundleTestConsumerBase(t testing.TB, clk clock.Clock) *outbox.ConsumerBase {
	t.Helper()

	cb, err := outbox.NewConsumerBase(
		idempotency.NewInMemClaimer(clk),
		outbox.ConsumerBaseConfig{},
		clk,
	)
	require.NoError(t, err)
	return cb
}
