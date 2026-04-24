package command

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Metadata size limits (META-SIZE-01)
//
// These constants prevent unbounded metadata from degrading broker throughput
// or exceeding transport-level control-line limits.
//
// ref: OTel sdk/trace/span_limits.go -- 128 attributes/span (GoCell uses 64
//      as a tighter balance between overhead prevention and practical use)
// ref: NATS server/const.go -- MAX_CONTROL_LINE_SIZE = 4096 bytes
// ref: RabbitMQ -- no hard header-size limit, but 64 KB total is a pragmatic
//      ceiling aligned with most broker implementations
// ---------------------------------------------------------------------------

const (
	// MaxMetadataKeys is the maximum number of key-value pairs in Entry.Metadata.
	// Typical GoCell entries carry 3-10 keys (trace_id, request_id, correlation_id
	// plus domain context); 64 provides 6x headroom while keeping serialized
	// overhead under 1 KB for small entries. OTel allows 128 attributes/span.
	MaxMetadataKeys = 64

	// MaxMetadataKeyLen is the maximum byte length of a single metadata key.
	// Measured in bytes (len()), not runes -- multi-byte UTF-8 keys are counted
	// by their wire size, consistent with transport-level limits.
	MaxMetadataKeyLen = 256

	// MaxMetadataValueLen is the maximum byte length of a single metadata value.
	// Aligned with NATS MAX_CONTROL_LINE_SIZE (4096). Measured in bytes.
	MaxMetadataValueLen = 4096

	// MaxMetadataTotalSize is the maximum total byte size of all metadata
	// key-value pairs combined (sum of len(k)+len(v) for each pair).
	MaxMetadataTotalSize = 65536
)

// validateMetadata checks metadata map against size limits.
// nil or empty metadata is valid (no checks needed).
func validateMetadata(m map[string]string) error {
	if len(m) == 0 {
		return nil
	}
	if len(m) > MaxMetadataKeys {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("command: metadata key count %d exceeds max %d", len(m), MaxMetadataKeys))
	}
	var total int
	for k, v := range m {
		if len(k) > MaxMetadataKeyLen {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("command: metadata key length %d exceeds max %d (key=%q)", len(k), MaxMetadataKeyLen, truncate(k, 64)))
		}
		if len(v) > MaxMetadataValueLen {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("command: metadata value length %d exceeds max %d (key=%q)", len(v), MaxMetadataValueLen, truncate(k, 64)))
		}
		total += len(k) + len(v)
	}
	if total > MaxMetadataTotalSize {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("command: metadata total size %d exceeds max %d", total, MaxMetadataTotalSize))
	}
	return nil
}

// truncate returns the first n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
