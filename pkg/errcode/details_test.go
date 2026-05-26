package errcode

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicDetailConstruction(t *testing.T) {
	t.Run("publicAttrCarriesKeyValue", func(t *testing.T) {
		d := PublicAttr("cellId", "abc-123")
		assert.Equal(t, "cellId", d.Key())
		assert.Equal(t, "abc-123", d.Value())
	})

	t.Run("zeroValueIsInert", func(t *testing.T) {
		var d PublicDetail
		assert.Equal(t, "", d.Key())
		assert.Nil(t, d.Value())
	})

	t.Run("acceptsAnyValueType", func(t *testing.T) {
		cases := []struct {
			name  string
			value any
		}{
			{"string", "v"},
			{"int", 42},
			{"int64", int64(42)},
			{"float64", 3.14},
			{"bool", true},
			{"duration", time.Second},
			{"time", time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				d := PublicAttr("k", tc.value)
				assert.Equal(t, tc.value, d.Value())
			})
		}
	})

	t.Run("asSlogAttr", func(t *testing.T) {
		d := PublicAttr("k", "v")
		attr := d.AsSlogAttr()
		assert.Equal(t, "k", attr.Key)
		assert.Equal(t, "v", attr.Value.Any())
	})
}

func TestPublicDetailMarshalJSON(t *testing.T) {
	t.Run("zeroValueWireShape", func(t *testing.T) {
		var d PublicDetail
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"","value":null}`, string(raw))
	})

	t.Run("stringValue", func(t *testing.T) {
		d := PublicAttr("deviceId", "abc-123")
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"deviceId","value":"abc-123"}`, string(raw))
	})

	t.Run("intValue", func(t *testing.T) {
		d := PublicAttr("retryCount", 3)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"retryCount","value":3}`, string(raw))
	})

	t.Run("boolValue", func(t *testing.T) {
		d := PublicAttr("retry", true)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"retry","value":true}`, string(raw))
	})

	t.Run("durationValueRendersAsNanoseconds", func(t *testing.T) {
		d := PublicAttr("timeout", time.Second)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"timeout","value":1000000000}`, string(raw))
	})
}

func TestInternalDetailConstruction(t *testing.T) {
	t.Run("internalAttrCarriesKeyValue", func(t *testing.T) {
		d := InternalAttr("query", "SELECT 1")
		assert.Equal(t, "query", d.Key())
		assert.Equal(t, "SELECT 1", d.Value())
	})

	t.Run("zeroValueIsInert", func(t *testing.T) {
		var d InternalDetail
		assert.Equal(t, "", d.Key())
		assert.Nil(t, d.Value())
	})

	t.Run("asSlogAttr", func(t *testing.T) {
		d := InternalAttr("query", "SELECT 1")
		attr := d.AsSlogAttr()
		assert.Equal(t, "query", attr.Key)
		assert.Equal(t, "SELECT 1", attr.Value.Any())
	})
}

func TestWithDetailsAndWithInternalAccumulate(t *testing.T) {
	t.Run("withDetailsAppendsAcrossCalls", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(PublicAttr("a", 1)),
			WithDetails(PublicAttr("b", 2), PublicAttr("c", 3)),
		)
		require.Len(t, err.Details, 3)
		assert.Equal(t, "a", err.Details[0].Key())
		assert.Equal(t, "b", err.Details[1].Key())
		assert.Equal(t, "c", err.Details[2].Key())
	})

	t.Run("withInternalAppendsAcrossCalls", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom",
			WithInternal(InternalAttr("op", "scan")),
			WithInternal(InternalAttr("key", "abc"), InternalAttr("retries", 3)),
		)
		require.Len(t, err.InternalDetails, 3)
		assert.Equal(t, "op", err.InternalDetails[0].Key())
		assert.Equal(t, "key", err.InternalDetails[1].Key())
		assert.Equal(t, "retries", err.InternalDetails[2].Key())
	})

	t.Run("withDetailsEmptyVariadicIsNoop", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad", WithDetails())
		assert.Nil(t, err.Details)
	})

	t.Run("withInternalEmptyVariadicIsNoop", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom", WithInternal())
		assert.Nil(t, err.InternalDetails)
	})
}

func TestErrorStringFormatsInternalDetails(t *testing.T) {
	t.Run("underscoreSentinelRendersBareValue", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom",
			WithInternal(InternalAttr("_", "scan error key=abc")))
		assert.Equal(t, "[ERR_INTERNAL] scan error key=abc", err.Error())
	})

	t.Run("multipleDetailsFormattedAsKeyValuePairs", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom",
			WithInternal(InternalAttr("op", "scan"), InternalAttr("retries", 3)))
		assert.Equal(t, "[ERR_INTERNAL] op=scan, retries=3", err.Error())
	})

	t.Run("fallbacksToMessageWhenNoInternal", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom")
		assert.Equal(t, "[ERR_INTERNAL] boom", err.Error())
	})
}

func TestFindAttrReturnsPublicDetail(t *testing.T) {
	t.Run("hitReturnsKeyAndValue", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(PublicAttr("reason", "expired")))
		d, ok := err.FindAttr("reason")
		require.True(t, ok)
		assert.Equal(t, "reason", d.Key())
		assert.Equal(t, "expired", d.Value())
	})

	t.Run("missReturnsZeroPublicDetail", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(PublicAttr("a", "1")))
		d, ok := err.FindAttr("missing")
		assert.False(t, ok)
		assert.Equal(t, "", d.Key())
		assert.Nil(t, d.Value())
	})

	t.Run("nilErrorReturnsFalse", func(t *testing.T) {
		var e *Error
		d, ok := e.FindAttr("anything")
		assert.False(t, ok)
		assert.Equal(t, PublicDetail{}, d)
	})
}

func TestErrorMarshalJSON5xxStripsDetails(t *testing.T) {
	t.Run("clientErrorPreservesDetails", func(t *testing.T) {
		err := New(KindNotFound, ErrCellNotFound, "cell not found",
			WithDetails(PublicAttr("cellId", "abc")))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, "ERR_CELL_NOT_FOUND", got["code"])
		details, ok := got["details"].([]any)
		require.True(t, ok)
		require.Len(t, details, 1)
		assert.Equal(t, "cellId", details[0].(map[string]any)["key"])
		assert.Equal(t, "abc", details[0].(map[string]any)["value"])
	})

	t.Run("serverErrorEmitsEmptyDetailsArray", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom",
			WithDetails(PublicAttr("dsn", "secret")))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, []any{}, got["details"])
		assert.NotContains(t, string(raw), "secret")
		assert.NotContains(t, string(raw), "dsn")
	})

	t.Run("internalDetailsNeverMarshal", func(t *testing.T) {
		err := New(KindNotFound, ErrCellNotFound, "cell not found",
			WithInternal(InternalAttr("query", "SELECT secret_token")))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		assert.NotContains(t, string(raw), "SELECT secret_token")
		assert.NotContains(t, string(raw), "internalDetails")
		assert.NotContains(t, string(raw), "query")
	})
}

// Compile-time assertion: WithDetails / WithInternal only accept their sealed
// newtypes. Passing raw slog.Attr or string would fail to compile, which is
// the Hard sealing invariant this refactor replaces DETAILS-SLOG-ATTR-01 with.
// This test is a positive smoke — the negative cases are not expressible.
func TestSealedSignatureSmoke(t *testing.T) {
	_ = WithDetails(PublicAttr("k", "v"))
	_ = WithInternal(InternalAttr("k", "v"))
	// Sanity: PublicDetail constructed via PublicAttr can be used with AsSlogAttr
	// for slog forwarding (HTTP error-logging middleware).
	d := PublicAttr("k", "v")
	attr := d.AsSlogAttr()
	assert.IsType(t, slog.Attr{}, attr)
}
