// Package auth: Authenticator contract.
//
// An Authenticator inspects an *http.Request and returns one of three outcomes:
//
//	(p, true, nil)                 — credential present and valid; caller stops the chain.
//	(absentPrincipal(), false, nil) — credential absent; caller should try the next authenticator.
//	(nil, false, err)              — credential present but invalid; caller MUST NOT fall through.
//
// ref: kubernetes/apiserver pkg/authentication/request/union/union.go (FailOnError=true)
package auth

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// callerCellPattern validates the caller cell id: must start with a lowercase
// letter and contain only lowercase letters, digits, and hyphens.
var callerCellPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

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

// UnionAuthenticator tries each child in order and returns the first successful
// result. An error from any child short-circuits the chain (FailOnError semantics).
type UnionAuthenticator struct {
	children []Authenticator
}

// NewUnionAuthenticator returns a UnionAuthenticator that delegates to children
// in the order provided.
func NewUnionAuthenticator(children ...Authenticator) *UnionAuthenticator {
	return &UnionAuthenticator{children: children}
}

// Authenticate iterates over child authenticators and returns the first result
// that indicates a valid credential. If a child returns an error the chain stops
// immediately and the error is propagated (credential present but invalid).
func (u *UnionAuthenticator) Authenticate(r *http.Request) (*Principal, bool, error) {
	for _, child := range u.children {
		p, ok, err := child.Authenticate(r)
		if err != nil {
			// Credential present but invalid — short-circuit, no fallthrough.
			return nil, false, err
		}
		if ok {
			// Credential valid — stop the chain.
			return p, true, nil
		}
		// Credential absent — try the next authenticator.
	}
	return absentPrincipal(), false, nil
}

// NewJWTAuthenticator returns an Authenticator that extracts a Bearer token
// from the Authorization header and verifies its "access" intent using v.
//
// Outcomes:
//
//	(p, true, nil)                 — token present and valid; Principal populated from claims.
//	(absentPrincipal(), false, nil) — no Authorization header, or non-Bearer scheme (let Union continue).
//	(nil, false, err)              — Bearer token present but VerifyIntent rejected it (short-circuit).
//
// ref: kubernetes/apiserver pkg/authentication/request/bearertoken/bearertoken.go
func NewJWTAuthenticator(v IntentTokenVerifier) Authenticator {
	return AuthenticatorFunc(func(r *http.Request) (*Principal, bool, error) {
		token := extractBearerToken(r)
		if token == "" {
			// No Bearer credential — absent, let the Union try the next authenticator.
			return absentPrincipal(), false, nil
		}
		claims, err := v.VerifyIntent(r.Context(), token, TokenIntentAccess)
		if err != nil {
			// Credential present but invalid — short-circuit.
			return nil, false, err
		}
		// G1.A: Reject tokens with an empty subject. An empty "sub" claim
		// indicates a JWT signing bug or OIDC misconfiguration; accepting it
		// would allow a bearer with roles to pass RequireAnyRole unchecked.
		if claims.Subject == "" {
			return nil, false, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "token subject missing")
		}
		return jwtClaimsToPrincipal(claims), true, nil
	})
}

// jwtClaimsToPrincipal converts verified JWT Claims to a Principal.
// Roles is a defensive copy so callers cannot mutate the underlying slice.
// The Claims map contains exactly three entries (sid, iss, token_use);
// other JWT fields (aud, exp, iat, …) are intentionally excluded.
func jwtClaimsToPrincipal(c Claims) *Principal {
	roles := append([]string(nil), c.Roles...)
	return &Principal{
		Kind:                  PrincipalUser,
		Subject:               c.Subject,
		Roles:                 roles,
		AuthMethod:            "jwt",
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
// service tokens (Authorization: ServiceToken <ts>:<nonce>:<mac>).
//
// Returns an error when:
//   - ring is nil or a typed-nil interface;
//   - no NonceStore was supplied via WithServiceTokenNonceStore;
//   - a NoopNonceStore (Kind() == NonceStoreKindNoop) was supplied — replay
//     protection is mandatory at every layer; dev/test wiring must use
//     InMemoryNonceStore (NewInMemoryNonceStore(ServiceTokenNonceTTL)).
//
// This aligns runtime/auth construction with the existing reject-Noop guards in
// kernel/cell.NewAuthServiceToken, runtime/bootstrap.auth_plan_validate, and
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
func NewServiceTokenAuthenticator(ring cell.HMACKeyring, clk clock.Clock, opts ...ServiceTokenOption) (Authenticator, error) {
	clock.MustHaveClock(clk, "auth.NewServiceTokenAuthenticator")
	cfg := serviceTokenConfig{clk: clk}
	for _, o := range opts {
		o(&cfg)
	}
	if validation.IsNilInterface(ring) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthKeyMissing,
			"auth: NewServiceTokenAuthenticator ring must not be nil")
	}
	if cfg.nonceStore == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"auth: NewServiceTokenAuthenticator requires a NonceStore via "+
				"WithServiceTokenNonceStore (use NewInMemoryNonceStore(ServiceTokenNonceTTL) for dev/test)")
	}
	if cfg.nonceStore.Kind() == NonceStoreKindNoop {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"auth: NewServiceTokenAuthenticator NonceStore must not be NonceStoreKindNoop; "+
				"service-token authenticators require replay protection at every layer")
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
//   - callerCell non-empty and matching [a-z][a-z0-9-]*
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
func verifyServiceTokenPayload(ring cell.HMACKeyring, payload string, cfg serviceTokenConfig, r *http.Request) (string, error) {
	parts := strings.SplitN(payload, ":", 4)
	switch len(parts) {
	case 2:
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "legacy 2-part service token format rejected")
	case 3:
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "legacy 3-part service token format rejected")
	}
	if len(parts) != 4 {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidServiceTokenFormat)
	}

	tsStr := parts[0]
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "invalid service token timestamp")
	}

	now := cfg.clk.Now()
	tokenTime := time.Unix(ts, 0)
	if tokenTime.After(now.Add(ServiceTokenClockSkew)) {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthTokenExpired, "service token timestamp is too far in the future")
	}
	age := now.Sub(tokenTime)
	if age >= ServiceTokenMaxAge {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthTokenExpired, "service token expired")
	}

	nonce, callerCell, sigHex := parts[1], parts[2], parts[3]
	if err := validateCallerCell(callerCell); err != nil {
		return "", err
	}

	message := buildServiceTokenMessage(r.Method, r.URL.Path, r.URL.RawQuery, tsStr, nonce, callerCell)

	providedMAC, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidServiceTokenFormat)
	}
	if !verifyServiceTokenMAC(ring, message, providedMAC) {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "invalid service token MAC")
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
// service token. The cell id must be non-empty and match [a-z][a-z0-9-]*.
func validateCallerCell(callerCell string) error {
	if callerCell == "" {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "caller cell missing")
	}
	if !callerCellPattern.MatchString(callerCell) {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			fmt.Sprintf("caller cell id %q invalid (must match ^[a-z][a-z0-9-]*$)", callerCell))
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
//	(absentPrincipal(), false, nil) — no Principal in ctx (chain may continue).
//
// This Authenticator never returns an error; callers that want a strict
// fail-closed mode should compose with a guard that rejects (false, nil)
// outcomes (the typical /api/v1/* listener already does this via JWT
// short-circuit).
//
// 警告：当挂载在已有 JWT listener 上时，ContextAuthenticator 应是 chain 中
// 唯一的 authenticator（不通过 UnionAuthenticator 组合）；absent-credential
// 结果（false, nil）不会阻止 Union 继续尝试下一个 authenticator，可能造成
// 意料之外的 fall-through 路径。
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
