package mqtt

import (
	"regexp"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// segmentRe validates a single segment of a client ID component (cellID, role,
// or instanceID). Rules: starts with a lowercase letter, followed by zero or
// more lowercase letters or digits, optionally followed by groups of "-" +
// one or more lowercase letters or digits.
var segmentRe = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// instanceSegmentRe is permissive enough to accept canonical RFC4122 UUID
// strings (uuid.NewString returns lowercase hex digits + hyphens) AND
// operator-supplied stable identifiers that follow the same lowercase
// hex/digit/letter/hyphen alphabet. It does not require the leading
// letter, so a UUID starting with a digit is accepted.
var instanceSegmentRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const (
	// maxSegmentLen is the maximum length of cellID or role individually.
	maxSegmentLen = 32
	// maxInstanceLen is the maximum length of instanceID. 64 accommodates
	// canonical UUIDs (36 chars) plus operator-supplied identifiers.
	maxInstanceLen = 64
	// maxClientIDLen is the maximum total length of the final ClientID string
	// (cellID + "-" + role + "-" + instanceID). MQTT v5 has no 23-char limit
	// but we cap defensively at 128 to stay broker-friendly.
	maxClientIDLen = 128

	msgInvalidClientID = "mqtt client id: cellID and role must be non-empty lowercase identifiers " +
		"(^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$, max 32 chars each); total max 128 chars"
	msgInvalidInstanceID = "mqtt client id: instanceID must match ^[a-z0-9]+(?:-[a-z0-9]+)*$, max 64 chars"
)

// ClientID is a validated MQTT client identifier. It is a sealed struct: the
// only way to obtain a non-zero ClientID is ParseEphemeralClientID or
// ParseStableClientID, which validate before constructing. ClientID{...} and
// any string conversion are impossible outside this package (unexported
// field), so an unvalidated ClientID cannot exist in a caller's hands.
//
// ref: errcode.PublicDetail sealed-value pattern (pkg/errcode/details.go)
type ClientID struct {
	value string
}

// ParseEphemeralClientID validates cellID and role, generates a fresh UUID
// instance suffix, and returns a ClientID in canonical form
// "{cellID}-{role}-{uuid}". Use this when SessionExpiry is 0 (clean session)
// so each connection gets a unique broker identity.
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
//
// ai-robust: typed function choice — picking Ephemeral vs Stable at the
// callsite makes "selecting the wrong session semantics" a name-level error
// visible to reviewers and AI co-authors.
func ParseEphemeralClientID(cellID, role string) (ClientID, error) {
	if err := validateCellRole(cellID, role); err != nil {
		return ClientID{}, err
	}
	return assembleClientID(cellID, role, uuid.NewString())
}

// ParseStableClientID validates cellID, role, and a caller-supplied instanceID
// and returns a ClientID in canonical form "{cellID}-{role}-{instanceID}".
// Use this when SessionExpiry > 0 (persistent session) so the broker can
// recover the session across reconnects.
//
// Validation rules:
//   - cellID/role: same as ParseEphemeralClientID
//   - instanceID: non-empty, length ≤ 64, match
//     ^[a-z0-9]+(?:-[a-z0-9]+)*$ (lowercase hex/digit/letter/hyphen;
//     accepts canonical UUIDs and operator-supplied identifiers)
//
// Total length of the resulting string must be ≤ 128.
//
// ai-robust: typed function choice — see ParseEphemeralClientID godoc.
func ParseStableClientID(cellID, role, instanceID string) (ClientID, error) {
	if err := validateCellRole(cellID, role); err != nil {
		return ClientID{}, err
	}
	if !validateInstance(instanceID) {
		return ClientID{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidClientID,
			msgInvalidInstanceID,
			errcode.WithDetails(errcode.PublicString("instanceID", instanceID)),
		)
	}
	return assembleClientID(cellID, role, instanceID)
}

// String returns the canonical "{cellID}-{role}-{instanceID}" string. Returns
// an empty string for the zero-value ClientID.
func (c ClientID) String() string { return c.value }

// validateCellRole reports the cellID/role validation error or nil.
func validateCellRole(cellID, role string) error {
	if !validateSegment(cellID) {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidClientID,
			msgInvalidClientID,
			errcode.WithDetails(errcode.PublicString("cellID", cellID)),
		)
	}
	if !validateSegment(role) {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidClientID,
			msgInvalidClientID,
			errcode.WithDetails(errcode.PublicString("role", role)),
		)
	}
	return nil
}

// assembleClientID concatenates the three segments and enforces the total
// length cap. Caller has already validated each segment.
func assembleClientID(cellID, role, instanceID string) (ClientID, error) {
	value := cellID + "-" + role + "-" + instanceID
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

// validateSegment reports whether s is a valid cellID/role segment.
// Returns false for empty, too long, or non-matching strings.
func validateSegment(s string) bool {
	if len(s) == 0 || len(s) > maxSegmentLen {
		return false
	}
	return segmentRe.MatchString(s)
}

// validateInstance reports whether s is a valid instanceID segment (canonical
// UUID or operator-supplied stable identifier).
func validateInstance(s string) bool {
	if len(s) == 0 || len(s) > maxInstanceLen {
		return false
	}
	return instanceSegmentRe.MatchString(s)
}
