package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
)

func TestConfigEntryResponse_RedactsSensitiveValues(t *testing.T) {
	now := time.Now()
	entry := &domain.ConfigEntry{
		ID: "cfg-1", Key: "db.password", Value: "s3cret!",
		Sensitive: true, Version: 2,
		CreatedAt: now, UpdatedAt: now,
	}
	resp := ToConfigEntryResponse(entry)

	assert.Equal(t, "cfg-1", resp.ID)
	assert.Equal(t, "db.password", resp.Key)
	assert.Equal(t, RedactedValue, resp.Value, "sensitive value must be redacted")
	assert.True(t, resp.Sensitive)
	assert.Equal(t, 2, resp.Version)

	// Verify the raw secret is not in serialized output.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "s3cret!")
	assert.Contains(t, string(b), RedactedValue)
}

func TestConfigEntryResponse_NonSensitivePreservesValue(t *testing.T) {
	now := time.Now()
	entry := &domain.ConfigEntry{
		ID: "cfg-2", Key: "app.name", Value: "gocell",
		Sensitive: false, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	resp := ToConfigEntryResponse(entry)

	assert.Equal(t, "gocell", resp.Value, "non-sensitive value must be preserved")
	assert.False(t, resp.Sensitive)
}

func TestConfigEntryResponse_CamelCaseKeys(t *testing.T) {
	now := time.Now()
	entry := &domain.ConfigEntry{
		ID: "cfg-3", Key: "app.name", Value: "v",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	resp := ToConfigEntryResponse(entry)

	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"key"`)
	assert.Contains(t, s, `"value"`)
	assert.Contains(t, s, `"sensitive"`)
	assert.Contains(t, s, `"version"`)
	assert.Contains(t, s, `"createdAt"`)
	assert.Contains(t, s, `"updatedAt"`)
}
