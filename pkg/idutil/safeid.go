package idutil

// SafeID is a stub. Wave 2 GREEN provides the real implementation.
// This stub exists so the RED-wave tests compile while still failing.
type SafeID string

// String returns the underlying string.
func (s SafeID) String() string { return string(s) }

// Validate is a stub that always returns nil (RED behavior).
func (s SafeID) Validate() error { return nil }

// UnmarshalJSON is a stub: it decodes the JSON string as-is without
// running IsSafeID / length checks. GREEN must replace this with a
// fail-closed validator. SafeID must implement encoding/json.Unmarshaler
// for the wire-boundary funnel claim to hold.
func (s *SafeID) UnmarshalJSON(data []byte) error {
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		*s = SafeID(data[1 : len(data)-1])
		return nil
	}
	*s = SafeID(data)
	return nil
}

// MarshalJSON emits s as a JSON string.
func (s SafeID) MarshalJSON() ([]byte, error) {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	out = append(out, []byte(s)...)
	out = append(out, '"')
	return out, nil
}

// ParseSafeID is a stub: returns SafeID(s) without validation. GREEN
// must apply IsSafeID + length checks.
func ParseSafeID(s string) (SafeID, error) {
	return SafeID(s), nil
}
