//go:build archtest_fixture

// Package webhookclaimerviolate is a RED fixture for
// WEBHOOK-IDEMPOTENCY-CLAIMER-01/A1: a `claim` that fabricates the idempotency
// receipt instead of sourcing it from r.claimer.Claim must be flagged. It mirrors
// the runtime/webhook claimed token shape (sealed struct with an rcpt field).
package webhookclaimerviolate

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/idempotency"
)

// claimed mirrors runtime/webhook.claimed: an unforgeable token carrying the
// idempotency receipt.
type claimed struct {
	rcpt idempotency.Receipt
}

// Receiver mirrors the runtime/webhook Receiver's claimer dependency.
type Receiver struct {
	claimer idempotency.Claimer
}

// claim FORGES the receipt with NonAcquiredReceipt instead of calling
// r.claimer.Claim — the data-flow violation the archtest must detect. The
// forged var is an Ident (so it passes a naive "rcpt must be an Ident" check),
// but its object is not bound from a Claimer.Claim call.
func (r *Receiver) claim(_ context.Context) claimed {
	forged := idempotency.NonAcquiredReceipt()
	return claimed{rcpt: forged}
}
