// Package audit centralizes bootstrap-period audit-chain helpers used by
// composition roots that wire runtime/auth.NewBootstrapMiddleware.
//
// The single dependency direction is runtime/audit → runtime/auth (type
// alias for BootstrapAuthFailObserver) plus runtime/audit → runtime/audit/ledger
// for the hash-chain Store interface. runtime/auth deliberately does not
// import this package, preserving the documented "runtime/auth must not
// depend on cells/ or higher-level audit machinery" rule.
//
// Hard / Medium funnel: see tools/archtest/bootstrap_audit_observer_funnel_test.go.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Bootstrap auth-fail reason values — these mirror the godoc-listed reason
// strings in runtime/auth/bootstrap.go (BootstrapAuthFailObserver). Keep both
// in sync; the typed string upgrade (`type BootstrapAuthFailReason string`)
// is tracked as backlog BOOTSTRAP-AUTHFAIL-REASON-TYPED-FUNNEL-01.
const (
	ReasonMissingHeader    = "missing_header"
	ReasonWrongCredentials = "wrong_credentials"
	ReasonRateLimited      = "rate_limited"

	// bootstrapAuthFailEventType is the ledger EventType label. Point-dotted
	// shape matches existing ledger.Entry godoc style ("user.login", "config.updated").
	bootstrapAuthFailEventType = "bootstrap.auth.fail"

	// bootstrapAuthFailActorID identifies the platform itself as the actor —
	// no real user is involved at bootstrap auth time.
	bootstrapAuthFailActorID = "system:bootstrap"
)

// validBootstrapAuthFailReasons is the whitelist that gates payload entry.
// Centralizing the set as a map keeps O(1) membership while making the three
// public constants the single source of truth.
var validBootstrapAuthFailReasons = map[string]struct{}{
	ReasonMissingHeader:    {},
	ReasonWrongCredentials: {},
	ReasonRateLimited:      {},
}

// bootstrapAuthFailPayload is the JSON envelope persisted in ledger.Entry.Payload.
// Field names are camelCase per go-standards JSON convention; future fields
// must be optional/additive (v1 schema-evolution rule for ledger payloads).
type bootstrapAuthFailPayload struct {
	Reason   string `json:"reason"`
	ClientIP string `json:"clientIp"`
}

// AppendBootstrapAuthFail constructs a bootstrap.auth.fail ledger entry and
// persists it via store.Append. clientIP may be empty when the request did
// not flow through middleware that sets ctxkeys.RealIP (e.g. health probes).
//
// Errors:
//   - ErrValidationFailed when store / clock is nil, or reason is not in
//     {missing_header, wrong_credentials, rate_limited}.
//   - The wrapped Append error otherwise — most commonly ledger duplicate
//     fingerprint or chain-write failure; callers (typically the observer
//     in NewBootstrapAuthFailObserver) log and continue.
func AppendBootstrapAuthFail(ctx context.Context, store ledger.Store, clk clock.Clock, reason, clientIP string) error {
	if validation.IsNilInterface(store) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: AppendBootstrapAuthFail requires non-nil ledger.Store",
			errcode.WithInternal("nil store"))
	}
	if validation.IsNilInterface(clk) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: AppendBootstrapAuthFail requires non-nil clock",
			errcode.WithInternal("nil clock"))
	}
	if _, ok := validBootstrapAuthFailReasons[reason]; !ok {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: bootstrap auth-fail reason not in whitelist",
			errcode.WithInternal(fmt.Sprintf(
				"reason=%q allowed={missing_header,wrong_credentials,rate_limited}", reason)))
	}
	payload, err := json.Marshal(bootstrapAuthFailPayload{Reason: reason, ClientIP: clientIP})
	if err != nil {
		// json.Marshal of two strings can only fail under engine-level
		// corruption; wrap for diagnostic surface and propagate.
		return fmt.Errorf("audit: marshal bootstrap auth-fail payload: %w", err)
	}
	entry := &ledger.Entry{
		EventID:   uuid.NewString(),
		EventType: bootstrapAuthFailEventType,
		ActorID:   bootstrapAuthFailActorID,
		Timestamp: clk.Now().UTC(),
		Payload:   payload,
	}
	if err := store.Append(ctx, entry); err != nil {
		return fmt.Errorf("audit: append bootstrap auth-fail entry: %w", err)
	}
	return nil
}
