// Package auth: Authenticator contract.
//
// An Authenticator inspects an *http.Request and returns one of three outcomes:
//
//	(p, true, nil)                 — credential present and valid; caller accepts the principal.
//	(absentPrincipal(), false, nil) — credential absent; caller decides (fail-closed by default).
//	(nil, false, err)              — credential present but invalid; caller MUST reject.
//
// Implementations are consumed directly (one Authenticator per mount point, e.g.
// the WebSocket upgrade slot); there is no fan-out combinator.
package auth

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// Authenticator inspects an HTTP request and resolves the caller's identity.
type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, bool, error)
}

// AuthenticatorFunc is a function that implements Authenticator.
type AuthenticatorFunc func(r *http.Request) (*Principal, bool, error)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(r *http.Request) (*Principal, bool, error) {
	return f(r)
}

func absentPrincipal() *Principal {
	return &Principal{}
}

// NewBearerHeaderAuthenticator returns an Authenticator that extracts a Bearer
// token from the Authorization header and delegates verification plus
// Principal construction to AuthenticateBearer.
//
// Outcomes:
//
//	(p, true, nil)                 — Bearer token present and valid.
//	(absentPrincipal(), false, nil) — no Authorization header, or non-Bearer scheme.
//	(nil, false, err)              — Bearer token present but verification rejected it.
//
// This is intentionally a single-scheme authenticator, not a chain/fan-out
// combinator. Callers that mount it beside other schemes should use separate
// listener/mount points rather than unioning multiple authenticators.
//
// ref: kubernetes/apiserver pkg/authentication/request/bearertoken/bearertoken.go
func NewBearerHeaderAuthenticator(v IntentTokenVerifier) Authenticator {
	return AuthenticatorFunc(func(r *http.Request) (*Principal, bool, error) {
		token, _ := extractBearerTokenWithReason(r)
		if token == "" {
			return absentPrincipal(), false, nil
		}
		_, p, err := AuthenticateBearer(r.Context(), v, token)
		if err != nil {
			return nil, false, err
		}
		return p, true, nil
	})
}

// jwtClaimsToPrincipal converts verified JWT Claims to a Principal. It dispatches
// on the verified, fail-closed-validated principal_kind claim: a device token
// (PrincipalKindClaimDevice) is minted through the sole sanctioned device issuer
// (mintDevicePrincipal, which fails closed on a missing subject/tenant or a
// privileged role); every other token is an ordinary user principal. This is the
// single bearer chokepoint shared by HTTP and gRPC, so device and user
// principals can only originate here — there is no separate forgeable path.
func jwtClaimsToPrincipal(c Claims) (*Principal, error) {
	if c.PrincipalKind == PrincipalKindClaimDevice {
		return mintDevicePrincipal(c)
	}
	return mintUserPrincipal(c), nil
}

// mintUserPrincipal builds an ordinary PrincipalUser from verified claims.
// Roles is a defensive copy so callers cannot mutate the underlying slice.
// The Claims map contains exactly three entries (sid, iss, token_use);
// other JWT fields (aud, exp, iat, …) are intentionally excluded. TenantID is
// carried in the dedicated Principal.TenantID field (already canonicalized by
// the verifier), not in the Claims map.
func mintUserPrincipal(c Claims) *Principal {
	roles := append([]string(nil), c.Roles...)
	return &Principal{
		Kind:                  PrincipalUser,
		Subject:               c.Subject,
		Roles:                 roles,
		AuthMethod:            "jwt",
		TenantID:              c.TenantID,
		PasswordResetRequired: c.PasswordResetRequired,
		Claims: map[string]string{
			"sid":       c.SessionID,
			"iss":       c.Issuer,
			"token_use": string(c.TokenUse),
		},
		ExpiresAt: c.ExpiresAt,
	}
}

// NewServiceTokenAuthenticator returns an Authenticator that validates HMAC
// service tokens (Authorization: ServiceToken <ts>:<nonce>:<callerCell>:<mac>).
//
// Returns an error when:
//   - ring is nil or a typed-nil interface;
//   - no NonceStore was supplied via WithServiceTokenNonceStore;
//   - a NoopNonceStore (Kind() == NonceStoreKindNoop) was supplied — replay
//     protection is mandatory at every layer; dev/test wiring must use
//     InMemoryNonceStore (NewInMemoryNonceStore(ServiceTokenNonceTTL, clock.Real())).
//
// This aligns runtime/auth construction with the existing reject-Noop guards in
// kernel/auth.NewAuthServiceToken, runtime/bootstrap.auth_plan_validate, and
// cmd/corebundle.SharedDeps.Validate — all four layers fail-closed.
//
// Outcomes (when construction succeeds):
//
//	(p, true, nil)                 — ServiceToken header present and valid.
//	(absentPrincipal(), false, nil) — no Authorization header, or non-"ServiceToken" scheme.
//	(nil, false, err)              — ServiceToken present but validation failed (expired, bad MAC, replay).
//
// ref: HashiCorp Vault server fail-closed defaults (no Noop-equivalent path).
// ref: kubernetes/apiserver pkg/authentication — typed (Authenticator, error).
func NewServiceTokenAuthenticator(ring kauth.HMACKeyring, clk clock.Clock, opts ...ServiceTokenOption) (Authenticator, error) {
	clock.MustHaveClock(clk, "auth.NewServiceTokenAuthenticator")
	cfg := serviceTokenConfig{clk: clk}
	for _, o := range opts {
		o(&cfg)
	}
	if validation.IsNilInterface(ring) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthKeyMissing,
			"auth: NewServiceTokenAuthenticator ring must not be nil")
	}
	// nonceStore is filtered via validation.IsNilInterface in WithServiceTokenNonceStore;
	// bare == nil here is sufficient because typed-nil cannot survive the option funnel.
	if cfg.nonceStore == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"auth: NewServiceTokenAuthenticator requires a NonceStore via "+
				"WithServiceTokenNonceStore (use NewInMemoryNonceStore("+
				"ServiceTokenNonceTTL, clock.Real()) for dev/test)")
	}
	if cfg.nonceStore.Kind() == NonceStoreKindNoop {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"auth: NewServiceTokenAuthenticator NonceStore must not be "+
				"NonceStoreKindNoop; service-token authenticators require replay "+
				"protection at every layer")
	}
	return AuthenticatorFunc(func(r *http.Request) (*Principal, bool, error) {
		raw := r.Header.Get("Authorization")
		if raw == "" {
			return absentPrincipal(), false, nil
		}
		scheme, payload, ok := strings.Cut(raw, " ")
		if !ok || !strings.EqualFold(scheme, "ServiceToken") {
			// Bearer or other scheme — absent for this authenticator.
			return absentPrincipal(), false, nil
		}
		payload = strings.TrimSpace(payload)
		callerCell, err := verifyServiceTokenPayload(ring, payload, cfg, r)
		if err != nil {
			return nil, false, err
		}
		return &Principal{
			Kind:         PrincipalService,
			CallerCellID: callerCell,
			AuthMethod:   "service_token",
		}, true, nil
	}), nil
}

// verifyServiceTokenPayload validates the raw payload portion of a ServiceToken
// header (everything after "ServiceToken "). It enforces:
//   - 4-part format: {timestamp}:{nonce}:{callerCell}:{hex_hmac}
//   - 3-part format explicitly rejected: "legacy 3-part service token format rejected"
//   - timestamp within ServiceTokenMaxAge
//   - callerCell non-empty and matching metadata.MatchCellID (metadata.CellIDPattern)
//   - HMAC valid for any key in ring
//   - nonce not replayed via NonceStore.CheckAndMark (Noop/nil stores are
//     rejected at construction time by NewServiceTokenAuthenticator)
//
// Returns the callerCell on success. Nonce replay errors preserve the original
// NonceStore error as the Cause so callers can inspect it with errors.Is (e.g.
// to distinguish ErrNonceReused from a store failure and map to the correct
// HTTP status code).
//
// This helper is intentionally package-private.
func verifyServiceTokenPayload(ring kauth.HMACKeyring, payload string, cfg serviceTokenConfig, r *http.Request) (string, error) {
	parts := strings.SplitN(payload, ":", 4)
	switch len(parts) {
	case 2:
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgLegacy2Part)
	case 3:
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgLegacy3Part)
	}
	if len(parts) != 4 {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidServiceTokenFormat)
	}

	tsStr := parts[0]
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidServiceTokenFormat)
	}

	now := cfg.clk.Now()
	tokenTime := time.Unix(ts, 0)
	if tokenTime.After(now.Add(ServiceTokenClockSkew)) {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthTokenExpired, msgFutureTimestamp)
	}
	age := now.Sub(tokenTime)
	if age >= ServiceTokenMaxAge {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthTokenExpired, msgExpired)
	}

	nonce, callerCell, sigHex := parts[1], parts[2], parts[3]
	if err := validateCallerCell(callerCell); err != nil {
		return "", err
	}

	// Fold the live X-Tenant-ID header into the MAC material (sign/verify share
	// buildServiceTokenMessage). A tampered, injected, or stripped tenant header
	// reconstructs a different message and fails verifyServiceTokenMAC below.
	message := buildServiceTokenMessage(r.Method, r.URL.Path, r.URL.RawQuery, tsStr, nonce, callerCell, r.Header.Get(HeaderTenantID))

	providedMAC, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidServiceTokenFormat)
	}
	if !verifyServiceTokenMAC(ring, message, providedMAC) {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidMAC)
	}

	// NonceStore.CheckAndMark is always a real replay-safe store (Noop rejected
	// at NewServiceTokenAuthenticator construction). Preserve the original
	// NonceStore error as Cause so callers can distinguish ErrNonceReused
	// (replay → 401) from store failures (→ 500).
	if err := cfg.nonceStore.CheckAndMark(r.Context(), nonce); err != nil {
		return "", errcode.Wrap(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "service token nonce check failed", err)
	}
	return callerCell, nil
}

// validateCallerCell validates the caller cell id extracted from the 4-part
// service token. The cell id must be non-empty and satisfy
// metadata.CellIDPattern (^[a-z][a-z0-9]+$). Single source — same regex
// the schema (cell.schema.json) and governance (FMT-C1) enforce.
func validateCallerCell(callerCell string) error {
	if callerCell == "" {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgCallerCellMissing)
	}
	if !metadata.MatchCellID(callerCell) {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			msgCallerCellInvalid,
			errcode.WithDetails(errcode.PublicString("callerCell", callerCell)))
	}
	return nil
}

// NewContextAuthenticator returns an Authenticator that extracts a Principal
// from the request context. It is used by adapters that mount behind an
// already-authenticated listener (e.g. WebSocket upgrade routes mounted on a
// JWT listener); the listener middleware writes the Principal via
// WithPrincipal, and the adapter's Authenticator simply reads it back.
//
// Outcomes:
//
//	(p, true, nil)                 — Principal found in ctx, Kind != PrincipalUnknown.
//	(absentPrincipal(), false, nil) — no Principal in ctx.
//
// This Authenticator never returns an error. The absent (false, nil) outcome
// means "the listener middleware did not stamp a Principal"; the consuming
// adapter MUST treat that as unauthenticated and fail closed (the WebSocket
// upgrade slot rejects with 401). It is mounted as the sole Authenticator for
// its endpoint — already-authenticated traffic from a JWT listener is the only
// expected source.
func NewContextAuthenticator() Authenticator {
	return AuthenticatorFunc(func(r *http.Request) (*Principal, bool, error) {
		if p, ok := FromContext(r.Context()); ok {
			return p, true, nil
		}
		return absentPrincipal(), false, nil
	})
}

// NewAnonymousAuthenticator returns an Authenticator that always succeeds
// with a fresh PrincipalAnonymous principal. It is the explicit, type-safe
// way to declare "this WebSocket endpoint accepts unauthenticated traffic"
// at the composition root — paired with UpgradeConfig.Authenticator's
// non-nil requirement to keep fail-closed semantics intact.
//
// The returned Principal carries Kind=PrincipalAnonymous and zero ExpiresAt
// (anonymous principals never expire). Subject and Roles are intentionally
// empty; downstream authorization (auth.RequireSelfOrRole etc.) treats the
// anonymous Principal as having no privileges.
func NewAnonymousAuthenticator() Authenticator {
	return AuthenticatorFunc(func(_ *http.Request) (*Principal, bool, error) {
		return &Principal{Kind: PrincipalAnonymous}, true, nil
	})
}
