package webhook

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// WebhookReceiveHandler is the inbound-webhook handler signature cellgen emits
// as the second argument to reg.RegisterWebhookReceiver. The runtime resolves
// the verified payload of an inbound webhook request and hands it to the
// handler; a non-nil error signals the receiver runtime to reject the delivery.
//
// PR-2 record-only seam: this signature is intentionally minimal (stdlib types
// only) because PR-2 closes the cellgen ↔ Registrar compile gap without wiring
// any runtime. The PR-3 receiver runtime MAY refine the signature (e.g. carry a
// richer Delivery value instead of raw []byte) — GoCell carries no
// backward-compat burden (CLAUDE.md), so PR-3 is free to evolve this type in
// lockstep with the cellgen template and the generated method values it binds.
type WebhookReceiveHandler func(ctx context.Context, payload []byte) error

// WebhookDispatchSelector is the outbound-webhook target-selector signature
// cellgen emits as the second argument to reg.RegisterWebhookDispatch. Given an
// outbound payload it returns the target identifier the dispatcher runtime uses
// to route and sign the request; a non-nil error signals the dispatcher to skip
// or fail the dispatch.
//
// PR-2 record-only seam: like [WebhookReceiveHandler] this is intentionally
// minimal (stdlib types only). The PR-5 dispatcher runtime MAY refine the
// signature (e.g. return a structured Target rather than a bare string) —
// GoCell carries no backward-compat burden (CLAUDE.md), so PR-5 is free to
// evolve this type alongside the cellgen template.
type WebhookDispatchSelector func(ctx context.Context, payload []byte) (string, error)

// ReceiverSpec is the pure-data descriptor cellgen emits into cell_gen.go to
// register an inbound webhook receiver. It carries only identifiers known at
// code-generation time — ContractID (slice.yaml contractUsages.contract),
// SourceID (slice.yaml contractUsages.sourceID, the secret-isolation key that
// selects the signing secret used to verify inbound requests from the external
// source), and CellID (cell.yaml id, injected as a literal so the observability
// owner traces to cell metadata, mirroring reg.Subscribe's positional cellID).
// The runtime path resolves everything else (HTTP route mount, signature config,
// Claimer) from contract metadata.
//
// The reg.RegisterWebhookReceiver Registrar method and its bootstrap drain land
// in PR-3 (receiver runtime). This struct is the cellgen ↔ runtime seam that
// PR-2 stabilizes so the generated literal references a real type rather than a
// forward reference; the dispatch counterpart is [DispatchSpec].
type ReceiverSpec struct {
	ContractID string
	// SourceID is the secret-isolation key: it selects the signing secret used
	// to verify the HMAC signature on inbound webhook requests from the external
	// source. Must match contract.yaml endpoints.inbound.sourceID.
	SourceID string
	CellID   string
}

// Validate reports an [errcode.ErrWebhookConfigInvalid] error when any required
// field is empty. The zero value is invalid.
func (s ReceiverSpec) Validate() error {
	return validateSpecFields(s.ContractID, s.SourceID, s.CellID)
}

// DispatchSpec is the pure-data descriptor cellgen emits to register an outbound
// webhook dispatcher. SourceID names the signing-secret source: it selects the
// signing secret used when signing outbound requests to the external target
// (the counterpart of ReceiverSpec.SourceID which selects the secret for
// verifying inbound requests). The reg.RegisterWebhookDispatch Registrar method
// and the dispatcher runtime land in PR-5.
type DispatchSpec struct {
	ContractID string
	// SourceID is the signing-secret source: it selects the secret used to
	// sign outbound webhook requests sent to the external target. The peer
	// concept is ReceiverSpec.SourceID, which selects the secret for verifying
	// inbound webhook requests.
	SourceID string
	CellID   string
}

// Validate reports an [errcode.ErrWebhookConfigInvalid] error when any required
// field is empty. The zero value is invalid.
func (s DispatchSpec) Validate() error {
	return validateSpecFields(s.ContractID, s.SourceID, s.CellID)
}

// validateSpecFields is the shared three-field guard for [ReceiverSpec] and
// [DispatchSpec]. Messages are const literals (MESSAGE-CONST-LITERAL-01); the
// offending field is named in the message itself, no runtime data leaks.
// When contractID is non-empty (i.e. the error comes from SourceID or CellID
// validation), it is included in the details to pinpoint which spec failed.
func validateSpecFields(contractID, sourceID, cellID string) error {
	switch {
	case contractID == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: spec requires a non-empty ContractID")
	case sourceID == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: spec requires a non-empty SourceID",
			errcode.WithDetails(errcode.PublicString("contractID", contractID)))
	case cellID == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: spec requires a non-empty CellID",
			errcode.WithDetails(errcode.PublicString("contractID", contractID)))
	default:
		return nil
	}
}
