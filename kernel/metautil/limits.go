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

	// DomainOutbox / DomainCommand are the only valid domainPrefix values
	// accepted by ValidateLimits. Adding a third transport requires extending
	// these constants AND every per-error helper switch below.
	DomainOutbox  = "outbox"
	DomainCommand = "command"

	internalKeyQuotedFmt   = "key=%q"
	internalValueKeyFmt    = "value_key=%q"
	internalValueValueFmt  = "value_truncated=%q"
	maxInternalQuotedBytes = 64
)

// ValidateLimits checks size/count/length limits on metadata. Returns nil
// for empty or nil input. domainPrefix MUST be one of DomainOutbox or
// DomainCommand — any other value yields an errcode.Assertion error
// (KindInternal) so callers see the contract violation surface instead of
// a silent no-prefix message. The prefix appears at the start of every
// validation-failure Message so failures stay traceable to the owning
// transport.
//
// Each per-error helper holds its own const literal messages for archtest
// MESSAGE-CONST-LITERAL-01 compliance — the message argument to
// errcode.New must be a string literal at the call site.
//
// ref: OTel sdk/trace/span_limits.go -- attribute count + value length caps.
func ValidateLimits(m map[string]string, domainPrefix string) error {
	if domainPrefix != DomainOutbox && domainPrefix != DomainCommand {
		return errcode.Assertion("metautil: unknown domain prefix %q (use DomainOutbox or DomainCommand)", domainPrefix)
	}
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

// The default branches in the four helpers below are unreachable in
// normal flow because ValidateLimits validates domainPrefix at entry.
// They return errcode.Assertion (KindInternal) rather than panic so we
// don't add new entries to the architectural-panic ADR whitelist for
// what is structurally guarded code.

func newKeyCountErr(prefix string, count int) error {
	details := errcode.WithDetails(slog.Int("count", count), slog.Int("max", MaxMetadataKeys))
	switch prefix {
	case DomainOutbox:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata key count exceeds max", details)
	case DomainCommand:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata key count exceeds max", details)
	default:
		return errcode.Assertion("metautil: newKeyCountErr unknown prefix %q", prefix)
	}
}

func newKeyLenErr(prefix, key string) error {
	details := errcode.WithDetails(slog.Int("length", len(key)), slog.Int("max", MaxMetadataKeyLen))
	internal := errcode.WithInternal(fmt.Sprintf(internalKeyQuotedFmt, Truncate(key, maxInternalQuotedBytes)))
	switch prefix {
	case DomainOutbox:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata key length exceeds max", details, internal)
	case DomainCommand:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata key length exceeds max", details, internal)
	default:
		return errcode.Assertion("metautil: newKeyLenErr unknown prefix %q", prefix)
	}
}

func newValueLenErr(prefix, key, value string) error {
	details := errcode.WithDetails(slog.Int("length", len(value)), slog.Int("max", MaxMetadataValueLen))
	// Use distinct labels so operators can tell apart "key too long" from
	// "value too long" by looking at the Internal field alone.
	internal := errcode.WithInternal(fmt.Sprintf("%s %s",
		fmt.Sprintf(internalValueKeyFmt, Truncate(key, maxInternalQuotedBytes)),
		fmt.Sprintf(internalValueValueFmt, Truncate(value, maxInternalQuotedBytes)),
	))
	switch prefix {
	case DomainOutbox:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata value length exceeds max", details, internal)
	case DomainCommand:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata value length exceeds max", details, internal)
	default:
		return errcode.Assertion("metautil: newValueLenErr unknown prefix %q", prefix)
	}
}

func newTotalSizeErr(prefix string, total int) error {
	details := errcode.WithDetails(slog.Int("total", total), slog.Int("max", MaxMetadataTotalSize))
	switch prefix {
	case DomainOutbox:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: metadata total size exceeds max", details)
	case DomainCommand:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command: metadata total size exceeds max", details)
	default:
		return errcode.Assertion("metautil: newTotalSizeErr unknown prefix %q", prefix)
	}
}
