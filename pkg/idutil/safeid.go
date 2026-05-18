package idutil

import (
	"encoding/json"
	"fmt"
)

// SafeID is a metadata identifier whose non-empty value is guaranteed to
// pass IsSafeID + MaxMetadataIDLen. The zero value (empty string) is the
// "absent" representation for optional fields; required-empty semantics
// live at the caller.
//
// The Hard funnel claim: every wire-decode path that lands in a SafeID
// field goes through UnmarshalJSON, which fail-closes on unsafe input.
// json.Unmarshal is the single decode entry point for outbox envelopes
// (kernel/outbox.UnmarshalEnvelope is the only caller across all
// transports), so a SafeID-typed field cannot accept unsafe bytes from
// the wire by construction.
//
// In-memory construction (SafeID(s), &SafeID{}) bypasses validation — it
// is permitted in trusted paths (MustNewEntryID, tests). The CWE-117 log
// injection threat addressed by this type lives strictly at the wire
// boundary; in-memory state is server-trusted.
//
// ref: pkg/idutil.IsSafeID — character set source (ASCII letters, digits,
// `._:/-`); pkg/idutil.MaxMetadataIDLen — length cap.
type SafeID string

// String returns the underlying string.
func (s SafeID) String() string { return string(s) }

// Validate returns nil for the zero value or any string that passes
// IsSafeID + length cap; otherwise returns a descriptive error.
func (s SafeID) Validate() error {
	return validateSafeIDString(string(s))
}

// UnmarshalJSON decodes data into s, returning an error if the JSON value
// is not a string OR if the decoded string fails Validate. Empty string
// is allowed (zero/absent semantic). Implements encoding/json.Unmarshaler.
func (s *SafeID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("idutil: SafeID: %w", err)
	}
	if err := validateSafeIDString(raw); err != nil {
		return err
	}
	*s = SafeID(raw)
	return nil
}

// MarshalJSON emits s as a JSON string. Implements encoding/json.Marshaler.
func (s SafeID) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

// ParseSafeID validates s and returns a SafeID. Empty input returns the
// zero value with no error.
func ParseSafeID(s string) (SafeID, error) {
	if err := validateSafeIDString(s); err != nil {
		return "", err
	}
	return SafeID(s), nil
}

// validateSafeIDString is the single source of truth for SafeID's
// invariant. Length is checked before charset so the error message
// surfaces the more economical mismatch first.
func validateSafeIDString(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > MaxMetadataIDLen {
		return fmt.Errorf("idutil: SafeID length %d exceeds max %d", len(s), MaxMetadataIDLen)
	}
	if !IsSafeID(s) {
		return fmt.Errorf("idutil: SafeID contains unsafe characters")
	}
	return nil
}
