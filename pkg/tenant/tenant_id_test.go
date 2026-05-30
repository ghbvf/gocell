package tenant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	validTenantUUID   = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	validTenantUUIDUp = "3F2504E0-4F89-41D3-9A0C-0305E82C3301"
)

func TestTenantID_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      TenantID
		wantErr bool
	}{
		{"valid uuid", TenantID(validTenantUUID), false},
		{"empty rejected", TenantID(""), true},
		{"non-uuid rejected", TenantID("not-a-uuid"), true},
		{"safe-charset but not uuid rejected", TenantID("acme-tenant"), true},
		{"uppercase uuid accepted by parser", TenantID(validTenantUUIDUp), false},
		{"injection chars rejected", TenantID("3f2504e0-4f89-41d3-9a0c-0305e82c3301\n"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.in.Validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseTenantID(t *testing.T) {
	t.Parallel()

	t.Run("valid uuid returns typed value", func(t *testing.T) {
		t.Parallel()
		got, err := ParseTenantID(validTenantUUID)
		require.NoError(t, err)
		assert.Equal(t, validTenantUUID, got.String())
	})

	t.Run("empty rejected (no absent semantic)", func(t *testing.T) {
		t.Parallel()
		_, err := ParseTenantID("")
		assert.Error(t, err)
	})

	t.Run("non-uuid rejected", func(t *testing.T) {
		t.Parallel()
		_, err := ParseTenantID("garbage")
		assert.Error(t, err)
	})

	t.Run("uppercase normalized to canonical lowercase", func(t *testing.T) {
		t.Parallel()
		got, err := ParseTenantID(validTenantUUIDUp)
		require.NoError(t, err)
		assert.Equal(t, validTenantUUID, got.String(), "ParseTenantID must canonicalize to lowercase")
	})
}

func TestTenantID_UnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		data    string
		want    TenantID
		wantErr bool
	}{
		{"valid uuid", `"` + validTenantUUID + `"`, TenantID(validTenantUUID), false},
		{"uppercase canonicalized", `"` + validTenantUUIDUp + `"`, TenantID(validTenantUUID), false},
		{"empty string rejected", `""`, "", true},
		{"non-uuid rejected", `"garbage"`, "", true},
		{"null rejected (no absent over wire)", `null`, "", true},
		{"non-string json rejected", `123`, "", true},
		{"malformed json rejected", `{`, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got TenantID
			err := json.Unmarshal([]byte(tc.data), &got)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTenantID_MarshalJSONRoundtrip(t *testing.T) {
	t.Parallel()
	orig := TenantID(validTenantUUID)
	b, err := json.Marshal(orig)
	require.NoError(t, err)
	assert.Equal(t, `"`+validTenantUUID+`"`, string(b))

	var back TenantID
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, orig, back)
}

func TestTenantID_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, validTenantUUID, TenantID(validTenantUUID).String())
	assert.Equal(t, "", TenantID("").String())
	assert.False(t, strings.Contains(TenantID(validTenantUUID).String(), `"`))
}
