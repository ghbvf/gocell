package initialadmin

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// GeneratePassword generates a cryptographically random password from 32 bytes
// of entropy, encoded as base64url without padding (no '=', '+', or '/').
//
// reader is the entropy source; pass nil to use crypto/rand.Reader (production
// default). Tests may inject iotest.ErrReader or a deterministic reader.
func GeneratePassword(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}

	buf := make([]byte, 32)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", fmt.Errorf("initialadmin: generate password: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
