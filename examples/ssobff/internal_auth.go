package main

import (
	"fmt"
	"os"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

const ssobffServiceKeyEnv = "GOCELL_SSOBFF_SERVICE_SECRET"

func newInternalAuthChainFromEnv() ([]kauth.ListenerAuth, error) {
	secret := os.Getenv(ssobffServiceKeyEnv)
	return newInternalAuthChain(secret)
}

func newInternalAuthChain(secret string) ([]kauth.ListenerAuth, error) {
	if secret == "" {
		return nil, fmt.Errorf("%s must be set for the internal listener", ssobffServiceKeyEnv)
	}

	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ssobffServiceKeyEnv, err)
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
