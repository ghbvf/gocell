package command

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetadataConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 64, MaxMetadataKeys)
	assert.Equal(t, 256, MaxMetadataKeyLen)
	assert.Equal(t, 4096, MaxMetadataValueLen)
	assert.Equal(t, 65536, MaxMetadataTotalSize)
}

func TestValidateMetadata_WithinLimits(t *testing.T) {
	t.Parallel()
	m := map[string]string{
		"trace_id":       "abc123",
		"correlation_id": "xyz789",
	}
	assert.NoError(t, validateMetadata(m))
}

func TestValidateMetadata_NilEmpty_OK(t *testing.T) {
	t.Parallel()
	assert.NoError(t, validateMetadata(nil))
	assert.NoError(t, validateMetadata(map[string]string{}))
}

func TestValidateMetadata_KeyCountExceeds(t *testing.T) {
	t.Parallel()
	m := make(map[string]string)
	for i := 0; i <= MaxMetadataKeys; i++ {
		m[strings.Repeat("k", i+1)] = "v"
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata key count")
}

func TestValidateMetadata_KeyLenExceeds(t *testing.T) {
	t.Parallel()
	m := map[string]string{
		strings.Repeat("k", MaxMetadataKeyLen+1): "v",
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata key length")
}

func TestValidateMetadata_ValueLenExceeds(t *testing.T) {
	t.Parallel()
	m := map[string]string{
		"key": strings.Repeat("v", MaxMetadataValueLen+1),
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata value length")
}

func TestValidateMetadata_TotalSizeExceeds(t *testing.T) {
	t.Parallel()
	// Build metadata whose total size exceeds MaxMetadataTotalSize but individual
	// key/value lengths are within limits.
	m := make(map[string]string)
	// Each entry: key=16 chars, value=4096 chars => 4112 bytes
	// 16 such entries = 65792 bytes > 65536
	for i := 0; i < 16; i++ {
		key := strings.Repeat("k", 16) + string(rune('a'+i))
		val := strings.Repeat("v", MaxMetadataValueLen)
		m[key] = val
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata total size")
}
