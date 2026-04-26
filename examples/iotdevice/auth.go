package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

const (
	iotdeviceServiceSecretEnv = "GOCELL_IOTDEVICE_SERVICE_SECRET"
	jwtIssuerEnv              = "GOCELL_JWT_ISSUER"
	jwtAudienceEnv            = "GOCELL_JWT_AUDIENCE"
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

	keySet, err := auth.LoadKeySetFromEnv()
	if err != nil {
		return nil, fmt.Errorf("load JWT key set from environment: %w", err)
	}
	verifier, err := auth.NewJWTVerifier(keySet,
		auth.WithExpectedAudiences(audience),
		auth.WithExpectedIssuer(issuer))
	if err != nil {
		return nil, fmt.Errorf("create JWT verifier: %w", err)
	}
	return verifier, nil
}

func newInternalAuthChainFromEnv() ([]cell.ListenerAuth, error) {
	secret := os.Getenv(iotdeviceServiceSecretEnv)
	if secret == "" {
		return nil, fmt.Errorf("%s must be set for the internal listener", iotdeviceServiceSecretEnv)
	}

	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", iotdeviceServiceSecretEnv, err)
	}
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL)
	if err != nil {
		return nil, fmt.Errorf("create internal listener nonce store: %w", err)
	}
	plan, err := cell.NewAuthServiceToken(store, ring)
	if err != nil {
		return nil, fmt.Errorf("build internal auth chain: %w", err)
	}
	return []cell.ListenerAuth{plan}, nil
}
