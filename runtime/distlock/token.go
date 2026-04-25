package distlock

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// randomToken generates a cryptographically secure random hex string suitable
// for use as a lock ownership token. Moved from adapters/redis to runtime/distlock
// so the token generation strategy is independent of the backend adapter.
//
// ref: adapters/redis/distlock.go randomToken — identical algorithm, hoisted up
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("distlock: random token generation failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}
