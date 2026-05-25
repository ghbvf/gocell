package distlock

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// randomToken generates a cryptographically secure random hex string suitable
// for use as a lock ownership token. Moved from adapters/redis to runtime/distlock
// so the token generation strategy is independent of the backend adapter.
//
// ref: adapters/redis/distlock.go randomToken — identical algorithm, hoisted up
func randomToken() (string, error) { return newRandomToken(rand.Reader) }

func newRandomToken(r io.Reader) (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("distlock: read entropy: %w", err)
	}
	return hex.EncodeToString(b), nil
}
