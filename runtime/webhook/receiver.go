package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Claim TTLs used by the receiver's idempotency Claimer.
const (
	// receiverLeaseTTL is how long the in-flight processing lease is held.
	// If the handler goroutine crashes mid-flight, the claimer reclaims after
	// this window.
	receiverLeaseTTL = 30 * time.Second

	// receiverDoneTTL is how long a successfully-committed idempotency key
	// is remembered. Matches the EventBus default (24 h).
	receiverDoneTTL = 24 * time.Hour
)

// verified is an unforgeable token produced only by [Receiver.verify].
// Package-external code cannot construct this type (unexported struct with no
// exported constructor), so [Receiver.claim] can require it as a parameter to
// guarantee that claim is never called without a preceding successful verify.
type verified struct{ d kwh.Delivery }

// claimed is an unforgeable token produced only by [Receiver.claim].
// [Receiver.invokeHandler] requires it, ensuring that the business handler is
// never invoked without a prior idempotency claim.
type claimed struct {
	d    kwh.Delivery
	rcpt idempotency.Receipt
}

// Receiver is an HTTP handler that implements the full inbound-webhook receive
// pipeline: body-size limit → HMAC verify → idempotency claim → handler
// dispatch → receipt commit/release.
//
// Construct via [NewReceiver]; the zero value is invalid.
type Receiver struct {
	clk      clock.Clock
	spec     kwh.ReceiverSpec
	verifier kwh.Verifier
	store    kwh.SourceStore
	claimer  idempotency.Claimer
	handler  kwh.WebhookReceiveHandler
}

// NewReceiver constructs a Receiver. All parameters are mandatory; any nil or
// invalid argument returns an [errcode.ErrWebhookConfigInvalid] error.
func NewReceiver(
	clk clock.Clock,
	spec kwh.ReceiverSpec,
	verifier kwh.Verifier,
	store kwh.SourceStore,
	claimer idempotency.Claimer,
	handler kwh.WebhookReceiveHandler,
) (*Receiver, error) {
	clock.MustHaveClock(clk, "webhook.NewReceiver")
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if validation.IsNilInterface(verifier) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook receiver: verifier must not be nil")
	}
	if validation.IsNilInterface(store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook receiver: store must not be nil")
	}
	if validation.IsNilInterface(claimer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook receiver: claimer must not be nil")
	}
	if handler == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook receiver: handler must not be nil")
	}
	return &Receiver{
		clk:      clk,
		spec:     spec,
		verifier: verifier,
		store:    store,
		claimer:  claimer,
		handler:  handler,
	}, nil
}

// ServeHTTP implements http.Handler. Pipeline:
//  1. Read body with max-bytes enforcement.
//  2. Verify HMAC signature (produces [verified] token).
//  3. Claim idempotency key (produces [claimed] token).
//  4. Dispatch handler or short-circuit based on claim state.
//  5. Commit or release receipt based on handler outcome.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// 1. Body limit + read.
	body, err := r.readBody(w, req)
	if err != nil {
		return // readBody already wrote the error response.
	}

	// 2. Verify — produces unforgeable verified token.
	v, err := r.verify(body, req)
	if err != nil {
		slog.WarnContext(ctx, "webhook receiver: signature verification failed",
			slog.String("source_id", r.spec.SourceID),
			slog.String("contract_id", r.spec.ContractID),
			slog.Any("error", err))
		httputil.WriteError(ctx, w, err)
		return
	}

	// 3. Claim — produces unforgeable claimed token.
	c, state, err := r.claim(ctx, v)
	if err != nil {
		// Claimer infrastructure fault → fail-closed 503.
		slog.ErrorContext(ctx, "webhook receiver: idempotency claim failed",
			slog.String("source_id", r.spec.SourceID),
			slog.String("contract_id", r.spec.ContractID),
			slog.Any("error", err))
		httputil.WriteError(ctx, w, errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"webhook receiver: idempotency service unavailable"))
		return
	}

	// 4. Branch on claim state.
	switch state {
	case idempotency.ClaimDone:
		// Already processed — idempotent 200 without re-invoking handler.
		writeAccepted(w)
		return

	case idempotency.ClaimBusy:
		// Another worker is currently processing — 409 so the sender retries.
		// ErrWebhookDuplicateDelivery (KindConflict → 409) is the semantically
		// correct sentinel: the delivery is a known duplicate, not a lease expiry.
		httputil.WriteError(ctx, w, errcode.New(errcode.KindConflict, errcode.ErrWebhookDuplicateDelivery,
			"webhook receiver: delivery is already being processed"))
		return

	case idempotency.ClaimAcquired:
		// 5. Dispatch handler (panic-safe — release lease before re-panic).
		r.dispatch(ctx, w, c)
	}
}

// dispatch invokes the business handler for an acquired [claimed] token.
// If the handler panics, dispatch releases the idempotency lease before
// re-panicking so the Claimer TTL does not permanently block re-delivery.
func (r *Receiver) dispatch(ctx context.Context, w http.ResponseWriter, c claimed) {
	defer func() {
		if rec := recover(); rec != nil {
			// C-class re-throw: release the lease so the sender can retry after
			// the recovery middleware converts the panic to a 500 response.
			releaseOnPanic(ctx, c, r.spec, string(c.d.DeliveryID))
			panic(panicregister.Approved("webhook-receiver-handler-panic-release", rec))
		}
	}()

	if err := r.invokeHandler(ctx, c); err != nil {
		releaseCtx := context.WithoutCancel(ctx)
		if releaseErr := c.rcpt.Release(releaseCtx); releaseErr != nil {
			slog.ErrorContext(ctx, "webhook receiver: receipt release failed",
				slog.String("source_id", r.spec.SourceID),
				slog.String("delivery_id", string(c.d.DeliveryID)),
				slog.Any("error", releaseErr))
		}
		httputil.WriteError(ctx, w, err)
		return
	}

	commitCtx := context.WithoutCancel(ctx)
	if commitErr := c.rcpt.Commit(commitCtx); commitErr != nil {
		// Commit failed — log but don't change the 200 that handler already
		// earned: the business operation succeeded; only the idempotency
		// record write failed (best-effort).
		slog.ErrorContext(ctx, "webhook receiver: receipt commit failed",
			slog.String("source_id", r.spec.SourceID),
			slog.String("delivery_id", string(c.d.DeliveryID)),
			slog.Any("error", commitErr))
	}
	writeAccepted(w)
}

// releaseOnPanic releases the idempotency lease using a cancel-safe context so
// the release call is not aborted if the request context was already canceled
// when the panic occurred.
func releaseOnPanic(ctx context.Context, c claimed, spec kwh.ReceiverSpec, deliveryID string) {
	releaseCtx := context.WithoutCancel(ctx)
	if err := c.rcpt.Release(releaseCtx); err != nil {
		slog.ErrorContext(ctx, "webhook receiver: receipt release failed after handler panic",
			slog.String("source_id", spec.SourceID),
			slog.String("delivery_id", deliveryID),
			slog.Any("error", err))
	}
}

// readBody applies MaxBytesReader and reads the full request body.
// On error it writes an appropriate response and returns a non-nil error;
// the caller must not write any further response.
func (r *Receiver) readBody(w http.ResponseWriter, req *http.Request) ([]byte, error) {
	ctx := req.Context()

	// Quick pre-check on Content-Length before allocating.
	if req.ContentLength > r.spec.MaxBodyBytes {
		httputil.WriteError(ctx, w, errcode.New(errcode.KindPayloadTooLarge,
			errcode.ErrWebhookBodyTooLarge, "webhook receiver: request body too large"))
		return nil, errcode.New(errcode.KindPayloadTooLarge, errcode.ErrWebhookBodyTooLarge,
			"webhook receiver: request body too large")
	}

	req.Body = http.MaxBytesReader(w, req.Body, r.spec.MaxBodyBytes)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httputil.WriteError(ctx, w, errcode.New(errcode.KindPayloadTooLarge,
				errcode.ErrWebhookBodyTooLarge, "webhook receiver: request body too large"))
			return nil, err
		}
		slog.ErrorContext(ctx, "webhook receiver: read body failed", slog.Any("error", err))
		httputil.WriteError(ctx, w, errcode.New(errcode.KindUnavailable,
			errcode.ErrServiceUnavailable, "webhook receiver: failed to read request body"))
		return nil, err
	}
	return body, nil
}

// verify reads the configured signature headers from req, looks up the source,
// and validates the HMAC. Returns an unforgeable [verified] token on success.
// Errors are already typed *errcode.Error ready for httputil.WriteError.
func (r *Receiver) verify(body []byte, req *http.Request) (verified, error) {
	deliveryIDRaw := req.Header.Get(r.spec.DeliveryIDHeader)
	timestampRaw := req.Header.Get(r.spec.TimestampHeader)
	signatureRaw := req.Header.Get(r.spec.SignatureHeader)

	if deliveryIDRaw == "" || timestampRaw == "" || signatureRaw == "" {
		return verified{}, errcode.New(errcode.KindInvalid, errcode.ErrWebhookInvalidHeader,
			"webhook receiver: one or more required signature headers are missing")
	}

	deliveryID, err := kwh.NewDeliveryID(deliveryIDRaw)
	if err != nil {
		return verified{}, err
	}

	headers := kwh.Headers{
		DeliveryID: deliveryID,
		Timestamp:  timestampRaw,
		Signature:  signatureRaw,
	}

	// Look up the source by spec.SourceID. Return ErrWebhookInvalidSignature
	// (not a source-enumeration error) to avoid disclosing whether the source
	// exists (WEBHOOK-HMAC-FUNNEL-01 F8/F9).
	src, ok := r.store.Lookup(kwh.SourceID(r.spec.SourceID))
	if !ok {
		return verified{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrWebhookInvalidSignature,
			"webhook receiver: signature verification failed",
			errcode.WithInternal(errcode.InternalAttr("source_id", r.spec.SourceID)))
	}

	if err := r.verifier.Verify(body, headers, src); err != nil {
		return verified{}, err
	}

	return verified{d: kwh.Delivery{
		DeliveryID: deliveryID,
		SourceID:   kwh.SourceID(r.spec.SourceID),
		Headers:    headers,
		Payload:    body,
	}}, nil
}

// claim uses the idempotency Claimer to acquire a processing lease keyed on
// the source+delivery pair. Requires a [verified] token so callers cannot
// invoke claim before verify.
func (r *Receiver) claim(ctx context.Context, v verified) (claimed, idempotency.ClaimState, error) {
	// Idempotency key "webhook:{sourceID}:{deliveryID}" — source-scoped to
	// prevent cross-source replay collision if two sources share a Claimer.
	key := "webhook:" + r.spec.SourceID + ":" + string(v.d.DeliveryID)
	state, rcpt, err := r.claimer.Claim(ctx, key, receiverLeaseTTL, receiverDoneTTL)
	if err != nil {
		return claimed{}, state, err
	}
	return claimed{d: v.d, rcpt: rcpt}, state, nil
}

// invokeHandler is the single site in this package that calls the business
// handler. It requires a [claimed] token, guaranteeing that the full
// verify → claim sequence has completed.
func (r *Receiver) invokeHandler(ctx context.Context, c claimed) error {
	return r.handler(ctx, c.d)
}

// writeAccepted writes the minimal success response for a processed delivery.
//
// This response is addressed to the external webhook sender (Stripe, GitHub,
// Svix …), not to a GoCell API consumer. It intentionally does NOT follow the
// GoCell standard {"data":{}} envelope — senders only depend on a 2xx status
// code to confirm receipt; the body {"status":"accepted"} aligns with the Stripe
// and Svix acknowledgement convention and makes the response self-describing in
// logs/traces without leaking internal API shape.
func writeAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	body, _ := json.Marshal(map[string]string{"status": "accepted"})
	_, _ = w.Write(body)
}

// Compile-time assertion: Receiver satisfies http.Handler.
var _ http.Handler = (*Receiver)(nil)
