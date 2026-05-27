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
	t.Run("publicStringCarriesKeyValue", func(t *testing.T) {
		d := PublicString("cellId", "abc-123")
		assert.Equal(t, "cellId", d.Key())
		assert.Equal(t, "abc-123", d.Value())
	})

	t.Run("zeroValueIsInert", func(t *testing.T) {
		var d PublicDetail
		assert.Equal(t, "", d.Key())
		assert.Nil(t, d.Value())
	})

	t.Run("typedConstructorsCoverWireSafeScalars", func(t *testing.T) {
		assert.Equal(t, "v", PublicString("k", "v").Value())
		assert.Equal(t, int64(42), PublicInt("k", 42).Value())
		assert.Equal(t, int64(42), PublicInt("k", int64(42)).Value())
		assert.Equal(t, int64(42), PublicInt("k", int32(42)).Value())
		assert.Equal(t, true, PublicBool("k", true).Value())
		assert.Equal(t, time.Second, PublicDuration("k", time.Second).Value())
		ts := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)
		assert.Equal(t, ts, PublicTime("k", ts).Value())
	})

	t.Run("asSlogAttr", func(t *testing.T) {
		d := PublicString("k", "v")
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
		d := PublicString("deviceId", "abc-123")
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"deviceId","value":"abc-123"}`, string(raw))
	})

	t.Run("intValue", func(t *testing.T) {
		d := PublicInt("retryCount", 3)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"retryCount","value":3}`, string(raw))
	})

	t.Run("boolValue", func(t *testing.T) {
		d := PublicBool("retry", true)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"retry","value":true}`, string(raw))
	})

	t.Run("durationValueRendersAsNanoseconds", func(t *testing.T) {
		d := PublicDuration("timeout", time.Second)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"timeout","value":1000000000}`, string(raw))
	})

	t.Run("timeValueRendersAsRFC3339Nano", func(t *testing.T) {
		ts := time.Date(2026, 5, 27, 12, 34, 56, 0, time.UTC)
		d := PublicTime("at", ts)
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		assert.JSONEq(t, `{"key":"at","value":"2026-05-27T12:34:56Z"}`, string(raw))
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
			WithDetails(PublicInt("a", 1)),
			WithDetails(PublicInt("b", 2), PublicInt("c", 3)),
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
			WithDetails(PublicString("reason", "expired")))
		d, ok := err.FindAttr("reason")
		require.True(t, ok)
		assert.Equal(t, "reason", d.Key())
		assert.Equal(t, "expired", d.Value())
	})

	t.Run("missReturnsZeroPublicDetail", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(PublicString("a", "1")))
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
			WithDetails(PublicString("cellId", "abc")))
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
			WithDetails(PublicString("dsn", "secret")))
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

// TestAsSlogAttrKindByValue verifies AsSlogAttr routes each scalar kind
// through the matching slog.Value constructor. String values must surface
// as slog.KindString so pkg/redaction.RedactSlogAttr's free-form
// RedactString scan can mask embedded key=value secrets — same invariant
// the retired DETAILS-SLOG-ATTR-01 archtest enforced via AST scan on
// slog.Any callsites.
func TestAsSlogAttrKindByValue(t *testing.T) {
	cases := []struct {
		name string
		d    PublicDetail
		kind slog.Kind
	}{
		{"string", PublicString("k", "v"), slog.KindString},
		{"int", PublicInt("k", 42), slog.KindInt64},
		{"bool", PublicBool("k", true), slog.KindBool},
		{"duration", PublicDuration("k", time.Second), slog.KindDuration},
		{"time", PublicTime("k", time.Now()), slog.KindTime},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.kind, tc.d.AsSlogAttr().Value.Kind())
		})
	}

	t.Run("internalDetailStringValueProducesKindString", func(t *testing.T) {
		attr := InternalAttr("k", "v").AsSlogAttr()
		assert.Equal(t, slog.KindString, attr.Value.Kind(),
			"string-valued InternalDetail must surface as slog.KindString for redaction coverage")
	})

	t.Run("internalDetailNonStringValueAny", func(t *testing.T) {
		attr := InternalAttr("count", 42).AsSlogAttr()
		// InternalDetail keeps untyped any; non-string surfaces via
		// slog.Any (resolved to KindInt64 for int by slog internals,
		// but the contract is "not KindString").
		assert.NotEqual(t, slog.KindString, attr.Value.Kind())
	})
}
