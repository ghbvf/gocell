package metautil

import (
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	// MaxMetadataKeys is the maximum number of key-value pairs in a metadata
	// map. Typical kernel entries carry 3-10 keys (trace_id, request_id,
	// correlation_id plus domain context); 64 provides 6x headroom while
	// keeping serialized overhead under 1 KB for small entries. OTel allows
	// 128 attributes/span.
	MaxMetadataKeys = 64

	// MaxMetadataKeyLen is the maximum byte length of a single metadata key.
	// Measured in bytes (len()), not runes -- multi-byte UTF-8 keys are
	// counted by their wire size, consistent with transport-level limits.
	MaxMetadataKeyLen = 256

	// MaxMetadataValueLen is the maximum byte length of a single metadata
	// value. Aligned with NATS MAX_CONTROL_LINE_SIZE (4096). Measured in
	// bytes.
	MaxMetadataValueLen = 4096

	// MaxMetadataTotalSize is the maximum total byte size of all metadata
	// key-value pairs combined (sum of len(k)+len(v) for each pair).
	MaxMetadataTotalSize = 65536

	internalKeyQuotedFmt = "key=%q"
)

// ValidateLimits checks size/count/length limits on metadata. Returns nil
// for empty or nil input. domainPrefix ("outbox", "command") is the
// caller's domain tag and appears at the start of every error Message so
// failures stay traceable to the owning transport without forcing each
// caller to reimplement the loop.
//
// Each per-error helper holds its own const literal messages for archtest
// MESSAGE-CONST-LITERAL-01 compliance — the message argument to
// errcode.New must be a string literal at the call site.
//
// ref: OTel sdk/trace/span_limits.go -- attribute count + value length caps.
func ValidateLimits(m map[string]string, domainPrefix string) error {
	if len(m) == 0 {
		return nil
	}
	if len(m) > MaxMetadataKeys {
		return newKeyCountErr(domainPrefix, len(m))
	}
	var total int
	for k, v := range m {
		if len(k) > MaxMetadataKeyLen {
			return newKeyLenErr(domainPrefix, k)
		}
		if len(v) > MaxMetadataValueLen {
			return newValueLenErr(domainPrefix, k, v)
		}
		total += len(k) + len(v)
	}
	if total > MaxMetadataTotalSize {
		return newTotalSizeErr(domainPrefix, total)
	}
	return nil
}

// Truncate returns the first n bytes of s, appending "..." if truncated.
// Used by callers building Internal-tagged debug attributes that should
// not exceed log-friendly lengths.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func newKeyCountErr(prefix string, count int) error {
	details := errcode.WithDetails(slog.Int("count", count), slog.Int("max", MaxMetadataKeys))
	switch prefix {
	case "outbox":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata key count exceeds max", details)
	case "command":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata key count exceeds max", details)
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"metadata key count exceeds max", details)
	}
}

func newKeyLenErr(prefix, key string) error {
	details := errcode.WithDetails(slog.Int("length", len(key)), slog.Int("max", MaxMetadataKeyLen))
	internal := errcode.WithInternal(fmt.Sprintf(internalKeyQuotedFmt, Truncate(key, 64)))
	switch prefix {
	case "outbox":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata key length exceeds max", details, internal)
	case "command":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata key length exceeds max", details, internal)
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"metadata key length exceeds max", details, internal)
	}
}

func newValueLenErr(prefix, key, value string) error {
	details := errcode.WithDetails(slog.Int("length", len(value)), slog.Int("max", MaxMetadataValueLen))
	internal := errcode.WithInternal(fmt.Sprintf(internalKeyQuotedFmt, Truncate(key, 64)))
	switch prefix {
	case "outbox":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata value length exceeds max", details, internal)
	case "command":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata value length exceeds max", details, internal)
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"metadata value length exceeds max", details, internal)
	}
}

func newTotalSizeErr(prefix string, total int) error {
	details := errcode.WithDetails(slog.Int("total", total), slog.Int("max", MaxMetadataTotalSize))
	switch prefix {
	case "outbox":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata total size exceeds max", details)
	case "command":
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata total size exceeds max", details)
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"metadata total size exceeds max", details)
	}
}
