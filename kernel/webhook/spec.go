package webhook

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Delivery carries the fully-verified context of an inbound webhook request.
// The receiver runtime populates a Delivery after reading the body, validating
// the HMAC signature, and claiming the delivery ID for idempotency. Handlers
// receive a Delivery and must not mutate its Payload slice.
type Delivery struct {
	// DeliveryID is the per-delivery identifier extracted from the configured
	// DeliveryIDHeader and validated before signature verification.
	DeliveryID DeliveryID

	// SourceID is the secret-isolation key that was used to verify this
	// delivery (i.e. the ReceiverSpec.SourceID that matched the inbound request).
	SourceID SourceID

	// Headers are the signature headers that were verified: DeliveryID,
	// Timestamp, and Signature. Re-using webhook.Headers avoids a parallel
	// type; the runtime has already checked all three fields before delivery.
	Headers Headers

	// Payload is the raw request body after signature verification. The slice
	// is owned by the receiver runtime; handlers must not retain it past the
	// handler call.
	Payload []byte
}

// WebhookReceiveHandler is the inbound-webhook handler signature cellgen emits
// as the second argument to reg.RegisterWebhookReceiver. The runtime hands the
// handler a Delivery that has already been HMAC-verified and idempotency-claimed;
// a non-nil error signals the receiver runtime to reject (NACK) the delivery.
//
// Handlers MUST be idempotent. The receiver's Claimer is defense-in-depth, not
// a sole guarantee: webhook delivery is at-least-once by nature (network
// retries, provider redelivery), and the dedupe window is bounded by the
// Claimer's done-TTL — a duplicate arriving after that window (e.g. a provider
// whose retry horizon exceeds the TTL) will re-invoke the handler. This matches
// the consumer-side guidance of Stripe / Svix, which leave deduplication to the
// application; GoCell additionally provides the Claimer as a framework-level
// best-effort layer.
//
// PR-3: signature upgraded from func(ctx, []byte) error to func(ctx, Delivery)
// error in lockstep with the cellgen template and the generated method values it
// binds. GoCell carries no backward-compat burden (CLAUDE.md).
type WebhookReceiveHandler func(ctx context.Context, d Delivery) error

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
// register an inbound webhook receiver. ContractID, SourceID, and CellID are
// the code-generation-time identifiers (stable since PR-2); PathPattern,
// DeliveryIDHeader, TimestampHeader, SignatureHeader, ToleranceSeconds, and
// MaxBodyBytes are the receiver runtime configuration that cellgen bakes from
// contract.yaml (signature / endpoints.inbound / payload sections) in batch B.
//
// The reg.RegisterWebhookReceiver Registrar method and its bootstrap drain land
// in PR-3 (receiver runtime). This struct is the cellgen ↔ runtime seam; the
// dispatch counterpart is [DispatchSpec].
type ReceiverSpec struct {
	ContractID string
	// SourceID is the secret-isolation key: it selects the signing secret used
	// to verify the HMAC signature on inbound webhook requests from the external
	// source. Must match contract.yaml endpoints.inbound.sourceID.
	SourceID string
	CellID   string

	// Runtime configuration — cellgen bakes these from contract.yaml fields.
	// PathPattern is the URL path pattern the receiver runtime mounts
	// (contract.yaml endpoints.inbound.pathPattern).
	PathPattern string
	// DeliveryIDHeader is the HTTP header name carrying the per-delivery
	// identifier (contract.yaml signature.deliveryIDHeader).
	DeliveryIDHeader string
	// TimestampHeader is the HTTP header name carrying the unix-seconds
	// timestamp that is part of the signed content
	// (contract.yaml signature.timestampHeader).
	TimestampHeader string
	// SignatureHeader is the HTTP header name carrying the space-separated
	// "v1,<base64>" HMAC tokens (contract.yaml signature.signatureHeader).
	SignatureHeader string
	// ToleranceSeconds is the bidirectional timestamp window in seconds within
	// which a signed delivery is accepted (contract.yaml
	// signature.toleranceSeconds). Must be > 0.
	ToleranceSeconds int64
	// MaxBodyBytes is the maximum accepted request body size in bytes
	// (contract.yaml payload.maxBodyBytes). Must be > 0.
	MaxBodyBytes int64
}

// Validate reports an [errcode.ErrWebhookConfigInvalid] error when any required
// field is empty or out of range. The zero value is invalid.
func (s ReceiverSpec) Validate() error {
	if err := validateSpecFields(s.ContractID, s.SourceID, s.CellID); err != nil {
		return err
	}
	switch {
	case s.PathPattern == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: ReceiverSpec requires a non-empty PathPattern",
			errcode.WithDetails(errcode.PublicString("contractID", s.ContractID)))
	case s.DeliveryIDHeader == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: ReceiverSpec requires a non-empty DeliveryIDHeader",
			errcode.WithDetails(errcode.PublicString("contractID", s.ContractID)))
	case s.TimestampHeader == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: ReceiverSpec requires a non-empty TimestampHeader",
			errcode.WithDetails(errcode.PublicString("contractID", s.ContractID)))
	case s.SignatureHeader == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: ReceiverSpec requires a non-empty SignatureHeader",
			errcode.WithDetails(errcode.PublicString("contractID", s.ContractID)))
	case s.ToleranceSeconds <= 0:
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: ReceiverSpec ToleranceSeconds must be positive",
			errcode.WithDetails(errcode.PublicString("contractID", s.ContractID)))
	case s.MaxBodyBytes <= 0:
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: ReceiverSpec MaxBodyBytes must be positive",
			errcode.WithDetails(errcode.PublicString("contractID", s.ContractID)))
	default:
		return nil
	}
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
