// Package auth: Authenticator contract.
//
// An Authenticator inspects an *http.Request and returns one of three outcomes:
//
//	(p, true, nil)    — credential present and valid; caller stops the chain.
//	(nil, false, nil) — credential absent; caller should try the next authenticator.
//	(nil, false, err) — credential present but invalid; caller MUST NOT fall through.
//
// ref: kubernetes/apiserver pkg/authentication/request/union/union.go (FailOnError=true)
package auth

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	return nil, false, nil
}

// NewJWTAuthenticator returns an Authenticator that extracts a Bearer token
// from the Authorization header and verifies its "access" intent using v.
//
// Outcomes:
//
//	(p, true, nil)    — token present and valid; Principal populated from claims.
//	(nil, false, nil) — no Authorization header, or non-Bearer scheme (let Union continue).
//	(nil, false, err) — Bearer token present but VerifyIntent rejected it (short-circuit).
//
// ref: kubernetes/apiserver pkg/authentication/request/bearertoken/bearertoken.go
func NewJWTAuthenticator(v IntentTokenVerifier) Authenticator {
	return AuthenticatorFunc(func(r *http.Request) (*Principal, bool, error) {
		token := extractBearerToken(r)
		if token == "" {
			// No Bearer credential — absent, let the Union try the next authenticator.
			return nil, false, nil
		}
		claims, err := v.VerifyIntent(r.Context(), token, TokenIntentAccess)
		if err != nil {
			// Credential present but invalid — short-circuit.
			return nil, false, err
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
	}
}

// NewServiceTokenAuthenticator returns an Authenticator that validates HMAC
// service tokens (Authorization: ServiceToken <ts>:<nonce>:<mac>).
//
// Outcomes:
//
//	(p, true, nil)    — ServiceToken header present and valid.
//	(nil, false, nil) — no Authorization header, or non-"ServiceToken" scheme.
//	(nil, false, err) — ServiceToken present but validation failed (expired, bad MAC, replay).
//
// Options reuse ServiceTokenOption so callers share the same clock/nonce/metrics
// injection points as ServiceTokenMiddleware.
func NewServiceTokenAuthenticator(ring *HMACKeyRing, opts ...ServiceTokenOption) Authenticator {
	cfg := serviceTokenConfig{now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	return AuthenticatorFunc(func(r *http.Request) (*Principal, bool, error) {
		raw := r.Header.Get("Authorization")
		if raw == "" {
			return nil, false, nil
		}
		scheme, payload, ok := strings.Cut(raw, " ")
		if !ok || !strings.EqualFold(scheme, "ServiceToken") {
			// Bearer or other scheme — absent for this authenticator.
			return nil, false, nil
		}
		payload = strings.TrimSpace(payload)
		if err := verifyServiceTokenPayload(ring, payload, cfg, r); err != nil {
			return nil, false, err
		}
		roles := append([]string(nil), BuiltinServiceRoles(ServiceNameInternal)...)
		return &Principal{
			Kind:       PrincipalService,
			Subject:    ServiceNameInternal,
			Roles:      roles,
			AuthMethod: "service_token",
		}, true, nil
	})
}

// verifyServiceTokenPayload validates the raw payload portion of a ServiceToken
// header (everything after "ServiceToken "). It enforces:
//   - 3-part format: {timestamp}:{nonce}:{hex_hmac}
//   - timestamp within ServiceTokenMaxAge
//   - HMAC valid for any key in ring
//   - nonce not replayed (if cfg.nonceStore is set)
//
// Errors use fmt.Errorf to avoid circular import of errcode; the HTTP layer
// maps these to ERR_AUTH_UNAUTHORIZED. This helper is intentionally package-private.
func verifyServiceTokenPayload(ring *HMACKeyRing, payload string, cfg serviceTokenConfig, r *http.Request) error {
	parts := strings.SplitN(payload, ":", 3)
	if len(parts) == 2 {
		return fmt.Errorf("auth: legacy 2-part service token format rejected")
	}
	if len(parts) != 3 {
		return fmt.Errorf("auth: %s", msgInvalidServiceTokenFormat)
	}

	tsStr := parts[0]
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("auth: invalid service token timestamp")
	}

	nowFn := cfg.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	tokenTime := time.Unix(ts, 0)
	age := now.Sub(tokenTime)
	if age < 0 {
		age = -age
	}
	if age >= ServiceTokenMaxAge {
		return fmt.Errorf("auth: service token expired")
	}

	nonce, sigHex := parts[1], parts[2]
	message := buildServiceTokenMessage(r.Method, r.URL.Path, r.URL.RawQuery, tsStr, nonce)

	providedMAC, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("auth: %s", msgInvalidServiceTokenFormat)
	}
	if !verifyServiceTokenMAC(ring, message, providedMAC) {
		return fmt.Errorf("auth: invalid service token MAC")
	}

	if cfg.nonceStore != nil {
		if err := cfg.nonceStore.CheckAndMark(r.Context(), nonce); err != nil {
			return fmt.Errorf("auth: service token replay detected: %w", err)
		}
	}
	return nil
}
