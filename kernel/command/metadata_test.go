package command

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metautil"
	"github.com/ghbvf/gocell/pkg/errcode"
)

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
	for i := 0; i <= metautil.MaxMetadataKeys; i++ {
		m[strings.Repeat("k", i+1)] = "v"
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command: metadata key count")
}

func TestValidateMetadata_KeyLenExceeds(t *testing.T) {
	t.Parallel()
	m := map[string]string{
		strings.Repeat("k", metautil.MaxMetadataKeyLen+1): "v",
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Contains(t, ecErr.Message, "command: metadata key length")
}

func TestValidateMetadata_ValueLenExceeds(t *testing.T) {
	t.Parallel()
	m := map[string]string{
		"key": strings.Repeat("v", metautil.MaxMetadataValueLen+1),
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Contains(t, ecErr.Message, "command: metadata value length")
}

func TestValidateMetadata_TotalSizeExceeds(t *testing.T) {
	t.Parallel()
	// Build metadata whose total size exceeds metautil.MaxMetadataTotalSize but
	// individual key/value lengths are within limits.
	m := make(map[string]string)
	// Each entry: key=16 chars, value=4096 chars => 4112 bytes
	// 16 such entries = 65792 bytes > 65536
	for i := range 16 {
		key := strings.Repeat("k", 16) + string(rune('a'+i))
		val := strings.Repeat("v", metautil.MaxMetadataValueLen)
		m[key] = val
	}
	err := validateMetadata(m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command: metadata total size")
}
