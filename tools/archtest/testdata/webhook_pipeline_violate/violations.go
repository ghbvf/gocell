// Package webhook_pipeline_violate is a synthetic violation fixture for the
// WEBHOOK-RECEIVER-PIPELINE-01 archtest rule. Each violation form is exercised
// once so TestWebhookReceiverPipeline_ReverseFixture can assert that A1 (token
// sealed-construction) and A2 (handler callsite allowlist) fire on each form.
//
// DO NOT use this package in production code.
package webhook_pipeline_violate

import "context"

// ── token type stubs (mirrors runtime/webhook unexported types) ──────────────
// These mirror the production verified/claimed types; the scanner checks the
// enclosing-function allowlist against the same struct names.

type delivery struct{ id string }
type receipt interface{ Commit(context.Context) error }

// verified mirrors runtime/webhook.verified — produced only by verify().
type verified struct{ d delivery }

// claimed mirrors runtime/webhook.claimed — produced only by claim().
type claimed struct {
	d    delivery
	rcpt receipt
}

type handler func(context.Context, delivery) error

type Receiver struct {
	handler handler
}

// ── legitimate construction sites (A1 must NOT fire here) ──────────────────

func (r *Receiver) verify(_ []byte) (verified, error) {
	// Legitimate: enclosing func IS "verify"
	return verified{d: delivery{id: "ok"}}, nil
}

func (r *Receiver) claim(_ context.Context, v verified) (claimed, error) {
	// Legitimate: enclosing func IS "claim"
	return claimed{d: v.d, rcpt: nil}, nil
}

func (r *Receiver) invokeHandler(ctx context.Context, c claimed) error {
	// Legitimate: enclosing func IS "invokeHandler"
	return r.handler(ctx, c.d)
}

// ── A1 VIOLATIONS: verified/claimed constructed outside allowed functions ────

// forgeBadVerified constructs verified{} outside of the verify function.
// VIOLATION A1 (verified{} outside "verify")
func forgeBadVerified() verified {
	return verified{d: delivery{id: "forged"}} // VIOLATION A1 — wrong enclosing func
}

// forgeBadClaimed constructs claimed{} outside of the claim function.
// VIOLATION A1 (claimed{} outside "claim")
func forgeBadClaimed() claimed {
	return claimed{d: delivery{id: "forged"}, rcpt: nil} // VIOLATION A1 — wrong enclosing func
}

// ── A2 VIOLATION: r.handler called outside invokeHandler ─────────────────────

// callHandlerDirectly calls r.handler outside of invokeHandler.
// VIOLATION A2 (r.handler called outside "invokeHandler")
func (r *Receiver) callHandlerDirectly(ctx context.Context, d delivery) error {
	return r.handler(ctx, d) // VIOLATION A2 — wrong enclosing func
}
