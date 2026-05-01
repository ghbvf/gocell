package initialadmin

import (
	"io"
	"time"
)

// credentialPayload holds the fields serialized into the credential file.
type credentialPayload struct {
	Username  string
	Password  string
	ExpiresAt time.Time
	// GeneratedAt is the wall-clock time at which the credential file was
	// created. Must be set by callers via an injected Clock; formatPayload
	// uses it directly so that the "Generated at" comment is consistent with
	// ExpiresAt.
	GeneratedAt time.Time
}

// writeCredentialFileOption is a functional option for writeCredentialFile.
type writeCredentialFileOption func(*writeCredentialFileConfig)

// writeCredentialFileConfig holds the resolved options for writeCredentialFile.
type writeCredentialFileConfig struct {
	// writer is called to serialize the payload into the temp file.
	// Defaults to formatPayload (the production serialiser).
	writer func(io.Writer, credentialPayload) error
}

// withPayloadWriter overrides the payload serialiser. Used in tests to inject
// a failing writer without OS-level tricks, replacing the old package-level
// payloadWriter variable (P2-8: parallel-test safety).
func withPayloadWriter(w func(io.Writer, credentialPayload) error) writeCredentialFileOption {
	return func(c *writeCredentialFileConfig) { c.writer = w }
}
