package main

import (
	"fmt"
	"os"
	"strings"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

const (
	iotdeviceServiceSecretEnv = "GOCELL_IOTDEVICE_SERVICE_SECRET"
	jwtIssuerEnv              = "GOCELL_JWT_ISSUER"
	jwtAudienceEnv            = "GOCELL_JWT_AUDIENCE"
)

func newJWTVerifierFromEnv(clk clock.Clock) (*auth.JWTVerifier, error) {
	issuer := strings.TrimSpace(os.Getenv(jwtIssuerEnv))
	if issuer == "" {
		return nil, fmt.Errorf("%s must be set", jwtIssuerEnv)
	}
	audience := strings.TrimSpace(os.Getenv(jwtAudienceEnv))
	if audience == "" {
		return nil, fmt.Errorf("%s must be set", jwtAudienceEnv)
	}

	keySet, err := auth.LoadKeySetFromEnv(clk)
	if err != nil {
		return nil, fmt.Errorf("load JWT key set from environment: %w", err)
	}
	verifier, err := auth.NewJWTVerifier(keySet, clk,
		auth.WithExpectedAudiences(audience),
		auth.WithExpectedIssuer(issuer))
	if err != nil {
		return nil, fmt.Errorf("create JWT verifier: %w", err)
	}
	return verifier, nil
}

func newInternalAuthChainFromEnv(clk clock.Clock) ([]kauth.ListenerAuth, error) {
	secret := os.Getenv(iotdeviceServiceSecretEnv)
	if secret == "" {
		return nil, fmt.Errorf("%s must be set for the internal listener", iotdeviceServiceSecretEnv)
	}

	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", iotdeviceServiceSecretEnv, err)
	}
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
	if err != nil {
		return nil, fmt.Errorf("create internal listener nonce store: %w", err)
	}
	plan, err := kauth.NewAuthServiceToken(store, ring)
	if err != nil {
		return nil, fmt.Errorf("build internal auth chain: %w", err)
	}
	return []kauth.ListenerAuth{plan}, nil
}
