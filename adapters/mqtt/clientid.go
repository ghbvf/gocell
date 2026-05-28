package mqtt

import (
	"regexp"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// segmentRe validates a single segment of a client ID component (cellID or role).
// Rules: starts with a lowercase letter, followed by zero or more lowercase
// letters or digits, optionally followed by groups of "-" + one or more
// lowercase letters or digits.
var segmentRe = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

const (
	// maxSegmentLen is the maximum length of cellID or role individually.
	maxSegmentLen = 32
	// maxClientIDLen is the maximum total length of the final ClientID string
	// (cellID + "-" + role + "-" + uuid). MQTT v5 has no 23-char limit but we
	// cap defensively at 128 to stay broker-friendly.
	maxClientIDLen = 128

	msgInvalidClientID = "mqtt client id: cellID and role must be non-empty lowercase identifiers " +
		"(^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$, max 32 chars each); total max 128 chars"
)

// ClientID is a validated MQTT client identifier. It is a sealed struct: the
// only way to obtain a non-zero ClientID is ParseClientID, which validates
// before constructing. ClientID{...} and any string conversion are impossible
// outside this package (unexported field), so an unvalidated ClientID cannot
// exist in a caller's hands.
//
// ref: errcode.PublicDetail sealed-value pattern (pkg/errcode/details.go)
type ClientID struct {
	value string
}

// ParseClientID validates cellID and role, then returns a ClientID in the
// canonical form "{cellID}-{role}-{uuid}". The uuid suffix guarantees broker
// uniqueness across instances of the same cell+role.
//
// Validation rules (both cellID and role):
//   - Non-empty, length ≤ 32
//   - Match ^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$ (lowercase, start letter,
//     segments separated by a single hyphen)
//
// Total length of the resulting string must be ≤ 128.
//
// Returns ErrAdapterMQTTInvalidClientID (KindInvalid) on validation failure.
// The offending values are placed in Details (not the message) to satisfy the
// MESSAGE-CONST-LITERAL-01 constraint.
func ParseClientID(cellID, role string) (ClientID, error) {
	if !validateSegment(cellID) {
		return ClientID{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidClientID,
			msgInvalidClientID,
			errcode.WithDetails(errcode.PublicString("cellID", cellID)),
		)
	}
	if !validateSegment(role) {
		return ClientID{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidClientID,
			msgInvalidClientID,
			errcode.WithDetails(errcode.PublicString("role", role)),
		)
	}

	uid := uuid.NewString()
	value := cellID + "-" + role + "-" + uid
	if len(value) > maxClientIDLen {
		return ClientID{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidClientID,
			msgInvalidClientID,
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("role", role),
				errcode.PublicInt("totalLen", len(value)),
				errcode.PublicInt("maxLen", maxClientIDLen),
			),
		)
	}
	return ClientID{value: value}, nil
}

// String returns the canonical "{cellID}-{role}-{uuid}" string. Returns an
// empty string for the zero-value ClientID.
func (c ClientID) String() string { return c.value }

// validateSegment reports whether s is a valid cellID/role segment.
// Returns false for empty, too long, or non-matching strings.
func validateSegment(s string) bool {
	if len(s) == 0 || len(s) > maxSegmentLen {
		return false
	}
	return segmentRe.MatchString(s)
}
