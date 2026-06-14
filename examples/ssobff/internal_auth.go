package main

import (
	"fmt"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/ghbvf/gocell/framework/runtime/auth"
)

const ssobffServiceKeyEnv = "GOCELL_SSOBFF_SERVICE_SECRET"

// newInternalAuthChain builds the service-token auth chain for the internal
// listener. secret must be non-empty (fail-fast). nonceStore is the topology-
// gated nonce store resolved by replaydeps.Resolve: in-memory for demo, Redis-
// backed for real multi-pod (so replay protection is cross-replica in real
// deployments).
func newInternalAuthChain(secret string, nonceStore kauth.NonceStore) ([]kauth.ListenerAuth, error) {
	if secret == "" {
		return nil, fmt.Errorf("%s must be set for the internal listener", ssobffServiceKeyEnv)
	}

	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ssobffServiceKeyEnv, err)
	}
	plan, err := kauth.NewAuthServiceToken(nonceStore, ring)
	if err != nil {
		return nil, fmt.Errorf("build internal auth chain: %w", err)
	}
	return []kauth.ListenerAuth{plan}, nil
}
