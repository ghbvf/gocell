package main

import (
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

const ssobffServiceSecretEnv = "GOCELL_SSOBFF_SERVICE_SECRET"

func newInternalAuthChainFromEnv() ([]cell.ListenerAuth, error) {
	secret := os.Getenv(ssobffServiceSecretEnv)
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
	return []cell.ListenerAuth{cell.NewAuthServiceToken(store, ring)}, nil
}
