package main

import (
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

const ssobffServiceSecretEnv = "GOCELL_SSOBFF_SERVICE_SECRET" //nolint:gosec // G101 false positive: env var name constant, not a hardcoded secret

func newInternalAuthChainFromEnv() ([]cell.ListenerAuth, error) {
	secret := os.Getenv(ssobffServiceSecretEnv)
	return newInternalAuthChain(secret)
}

func newInternalAuthChain(secret string) ([]cell.ListenerAuth, error) {
	if secret == "" {
		return nil, fmt.Errorf("%s must be set for the internal listener", ssobffServiceSecretEnv)
	}

	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ssobffServiceSecretEnv, err)
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
