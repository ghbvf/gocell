//go:build archtest_fixture

// Package webhooksourcecryptofixture is the WEBHOOK-SOURCE-CRYPTO-FUNNEL-01
// negative fixture. It calls the two sealed webhook-secret crypto entry points —
// kwh.Source.Encrypt and kwh.NewSourceFromCiphertext — from functions that are
// NOT the sanctioned persistence repo, exercising every use shape the rule
// catches: direct call and function/method-value capture.
//
// Loaded only when the archtest_fixture build tag is set (so the production
// Production scan never compiles it). Never imported from production code. The
// scanner pointed at this package with the real caller-allowlist must report one
// violation per use below:
//
//  1. badEncrypt          — direct call s.Encrypt(...).
//  2. badDecrypt          — direct call kwh.NewSourceFromCiphertext(...).
//  3. badEncryptCapture   — method-value capture m := s.Encrypt.
//  4. badDecryptCapture   — function-value capture fn := kwh.NewSourceFromCiphertext.
//
// Total: 4 violations. A caller in any of these positions could supply its own
// ValueTransformer and capture the raw secret at vt.Encrypt — exactly what the
// caller-allowlist forecloses.
//
// DO NOT use this package in production code.
package webhooksourcecryptofixture

import (
	"context"

	kwh "github.com/ghbvf/gocell/framework/kernel/webhook"
)

// badEncrypt calls Source.Encrypt outside the sanctioned repo — a caller here
// could pass a capturing transformer and read the plaintext secret. VIOLATION.
func badEncrypt(ctx context.Context, s kwh.Source) {
	_, _ = s.Encrypt(ctx, nil) // VIOLATION: non-repo caller of Source.Encrypt
}

// badDecrypt calls NewSourceFromCiphertext outside the sanctioned repo.
// VIOLATION.
func badDecrypt(ctx context.Context) {
	_, _ = kwh.NewSourceFromCiphertext(ctx, nil, kwh.SourceID("github"), nil, "", nil, nil) // VIOLATION
}

// badEncryptCapture captures Source.Encrypt as a method value — the function-
// value-capture bypass form (a direct-call-only scan would miss it). VIOLATION.
func badEncryptCapture(s kwh.Source) {
	m := s.Encrypt // VIOLATION: method-value capture of Source.Encrypt
	_ = m
}

// badDecryptCapture captures NewSourceFromCiphertext as a function value — the
// function-value-capture bypass form. VIOLATION.
func badDecryptCapture() {
	fn := kwh.NewSourceFromCiphertext // VIOLATION: function-value capture
	_ = fn
}
