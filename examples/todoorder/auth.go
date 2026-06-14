package main

import (
	"fmt"
	"os"
	"strings"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/ghbvf/gocell/adapters/ratelimit"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

const (
	todoorderServiceSecretEnv = "GOCELL_TODOORDER_SERVICE_SECRET"
	jwtIssuerEnv              = "GOCELL_JWT_ISSUER"
	jwtAudienceEnv            = "GOCELL_JWT_AUDIENCE"
	operatorAdminUsernameEnv  = "GOCELL_OPERATOR_ADMIN_USERNAME"
	operatorAdminPasswordEnv  = "GOCELL_OPERATOR_ADMIN_PASSWORD"
)

func newJWTVerifierFromEnv() (*auth.JWTVerifier, error) {
	issuer := strings.TrimSpace(os.Getenv(jwtIssuerEnv))
	if issuer == "" {
		return nil, fmt.Errorf("%s must be set", jwtIssuerEnv)
	}
	audience := strings.TrimSpace(os.Getenv(jwtAudienceEnv))
	if audience == "" {
		return nil, fmt.Errorf("%s must be set", jwtAudienceEnv)
	}

	keySet, err := auth.LoadKeySetFromEnv(clock.Real())
	if err != nil {
		return nil, fmt.Errorf("load JWT key set from environment: %w", err)
	}
	verifier, err := auth.NewJWTVerifier(keySet, clock.Real(),
		auth.WithExpectedAudiences(audience),
		auth.WithExpectedIssuer(issuer))
	if err != nil {
		return nil, fmt.Errorf("create JWT verifier: %w", err)
	}
	return verifier, nil
}

func newInternalAuthChainFromEnv() ([]kauth.ListenerAuth, error) {
	secret := os.Getenv(todoorderServiceSecretEnv)
	if secret == "" {
		return nil, fmt.Errorf("%s must be set for the internal listener", todoorderServiceSecretEnv)
	}

	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", todoorderServiceSecretEnv, err)
	}
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	if err != nil {
		return nil, fmt.Errorf("create internal listener nonce store: %w", err)
	}
	plan, err := kauth.NewAuthServiceToken(store, ring)
	if err != nil {
		return nil, fmt.Errorf("build internal auth chain: %w", err)
	}
	return []kauth.ListenerAuth{plan}, nil
}

// newOperatorAuthFromEnv builds the AdminListener operator-credential auth plan
// (AuthOperator) from GOCELL_OPERATOR_ADMIN_USERNAME / _PASSWORD. operator→system
// admin control-plane credentials, separate from the cell→cell internal listener
// (service token) and the public JWT listener.
//
// When either env var is unset the operator control-plane is left unconfigured
// (ok=false) so the demo still starts out of the box without exposing an admin
// port; the projection rebuild endpoint then stays programmatic-only (mirrors
// the readyz-verbose opt-in in run.go). When set, the plan is gated by a per-IP
// token-bucket rate limiter (defeats credential brute-force).
func newOperatorAuthFromEnv() (kauth.AuthOperator, bool, error) {
	username := strings.TrimSpace(os.Getenv(operatorAdminUsernameEnv))
	password := os.Getenv(operatorAdminPasswordEnv) // not trimmed — passwords may contain whitespace
	if username == "" || password == "" {
		return kauth.AuthOperator{}, false, nil
	}
	limiter := ratelimit.New(ratelimit.Config{Rate: 1, Burst: 5}, clock.Real())
	// onAuthFail is nil in this demo. Production consumers should pass a non-nil
	// observer to route operator auth failures (401/429) to an audit log for
	// brute-force visibility (see runtime/auth.BootstrapAuthFailObserver).
	plan, err := kauth.NewAuthOperator([]byte(username), []byte(password), limiter, nil)
	if err != nil {
		return kauth.AuthOperator{}, false, fmt.Errorf("build operator admin auth: %w", err)
	}
	return plan, true, nil
}
