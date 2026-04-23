package initialadmin

import (
	"io"
	"time"
)

// CredentialPayload holds the fields serialised into the credential file.
type CredentialPayload struct {
	Username  string
	Password  string
	ExpiresAt time.Time
}

// WriteCredentialFileOption is a functional option for WriteCredentialFile.
type WriteCredentialFileOption func(*writeCredentialFileConfig)

// writeCredentialFileConfig holds the resolved options for WriteCredentialFile.
type writeCredentialFileConfig struct {
	// writer is called to serialise the payload into the temp file.
	// Defaults to formatPayload (the production serialiser).
	writer func(io.Writer, CredentialPayload) error
}

// withPayloadWriter overrides the payload serialiser. Used in tests to inject
// a failing writer without OS-level tricks, replacing the old package-level
// payloadWriter variable (P2-8: parallel-test safety).
func withPayloadWriter(w func(io.Writer, CredentialPayload) error) WriteCredentialFileOption {
	return func(c *writeCredentialFileConfig) { c.writer = w }
}
