package errcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errcodeAsError struct {
	target *Error
}

func (e errcodeAsError) Error() string {
	return "custom as wrapper"
}

func (e errcodeAsError) As(target any) bool {
	ec, ok := target.(**Error)
	if !ok {
		return false
	}
	*ec = e.target
	return true
}

// TestWebhookSentinelCodes locks the wire code string of every webhook sentinel
// (KERNEL-WEBHOOK-01). Sentinels carry no intrinsic Kind — Kind is set at the
// construction site (see kernel/webhook); this test only freezes the code
// strings so renames are caught. The intended Kind→HTTP mapping is verified at
// the real construction points in kernel/webhook/webhook_test.go.
func TestWebhookSentinelCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code Code
		want string
	}{
		{ErrWebhookInvalidSignature, "ERR_WEBHOOK_INVALID_SIGNATURE"},
		{ErrWebhookTimestampExpired, "ERR_WEBHOOK_TIMESTAMP_EXPIRED"},
		{ErrWebhookDuplicateDelivery, "ERR_WEBHOOK_DUPLICATE_DELIVERY"},
		{ErrWebhookInvalidHeader, "ERR_WEBHOOK_INVALID_HEADER"},
		{ErrWebhookAlgorithmUnsupported, "ERR_WEBHOOK_ALGORITHM_UNSUPPORTED"},
		{ErrWebhookSourceNotFound, "ERR_WEBHOOK_SOURCE_NOT_FOUND"},
		{ErrWebhookSSRFBlocked, "ERR_WEBHOOK_SSRF_BLOCKED"},
		{ErrWebhookDeliveryFailed, "ERR_WEBHOOK_DELIVERY_FAILED"},
		{ErrWebhookDeliveryTimeout, "ERR_WEBHOOK_DELIVERY_TIMEOUT"},
		{ErrWebhookPermanentFailure, "ERR_WEBHOOK_PERMANENT_FAILURE"},
		{ErrWebhookBodyTooLarge, "ERR_WEBHOOK_BODY_TOO_LARGE"},
		{ErrWebhookConfigInvalid, "ERR_WEBHOOK_CONFIG_INVALID"},
	}
	if len(cases) != 12 {
		t.Fatalf("expected 12 webhook sentinels, got %d", len(cases))
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, string(tc.code))
		})
	}
}

func TestNewWrapAndOptions(t *testing.T) {
	cause := errors.New("pool exhausted")
	err := Wrap(
		KindUnavailable,
		ErrServiceUnavailable,
		"service unavailable",
		cause,
		WithInternal(InternalAttr("_", "postgres pool exhausted")),
		WithDetails(PublicBool("retry", true)),
		WithCategory(CategoryInfra),
	)

	assert.Equal(t, KindUnavailable, err.Kind)
	assert.Equal(t, ErrServiceUnavailable, err.Code)
	assert.Equal(t, "service unavailable", err.Message)
	require.Len(t, err.InternalDetails, 1)
	assert.Equal(t, "_", err.InternalDetails[0].Key())
	assert.Equal(t, "postgres pool exhausted", err.InternalDetails[0].Value())
	require.Len(t, err.Details, 1)
	assert.Equal(t, "retry", err.Details[0].Key())
	assert.Equal(t, true, err.Details[0].Value())
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, "[ERR_SERVICE_UNAVAILABLE] postgres pool exhausted: pool exhausted", err.Error())
}

func TestKindStatusAndPublicCode(t *testing.T) {
	cases := []struct {
		kind       Kind
		status     int
		publicCode Code
		client     bool
	}{
		{KindInvalid, http.StatusBadRequest, ErrInternal, true},
		{KindUnauthenticated, http.StatusUnauthorized, ErrInternal, true},
		{KindPermissionDenied, http.StatusForbidden, ErrInternal, true},
		{KindNotFound, http.StatusNotFound, ErrInternal, true},
		{KindConflict, http.StatusConflict, ErrInternal, true},
		{KindGone, http.StatusGone, ErrInternal, true},
		{KindPayloadTooLarge, http.StatusRequestEntityTooLarge, ErrInternal, true},
		{KindRateLimited, http.StatusTooManyRequests, ErrInternal, true},
		{KindClientClosed, StatusClientClosedRequest, ErrInternal, true},
		{KindUnavailable, http.StatusServiceUnavailable, ErrServiceUnavailable, false},
		{KindDeadlineExceeded, http.StatusGatewayTimeout, ErrServerTimeout, false},
		{KindInternal, http.StatusInternalServerError, ErrInternal, false},
		{KindNotImplemented, http.StatusNotImplemented, ErrInternal, false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			assert.Equal(t, tc.status, tc.kind.Status())
			assert.Equal(t, tc.publicCode, tc.kind.PublicCode())
			assert.Equal(t, tc.client, tc.kind.IsClient())
		})
	}
}

func TestErrorStatusAndPublicCode(t *testing.T) {
	clientErr := New(KindNotFound, ErrCellNotFound, "cell not found")
	assert.Equal(t, http.StatusNotFound, clientErr.Status())
	assert.Equal(t, ErrCellNotFound, clientErr.PublicCode())

	serverErr := New(KindUnavailable, ErrKeyProviderTransient, "vault sealed")
	assert.Equal(t, http.StatusServiceUnavailable, serverErr.Status())
	assert.Equal(t, ErrServiceUnavailable, serverErr.PublicCode())
}

func TestIsInfraError(t *testing.T) {
	assert.False(t, IsInfraError(nil))
	assert.True(t, IsInfraError(context.Canceled))
	assert.True(t, IsInfraError(context.DeadlineExceeded))
	assert.True(t, IsInfraError(errors.New("plain")))
	assert.True(t, IsInfraError(New(KindInternal, ErrInternal, "db", WithCategory(CategoryInfra))))
	assert.True(t, IsInfraError(New(KindInternal, ErrInternal, "unknown")))
	assert.False(t, IsInfraError(New(KindNotFound, ErrSessionNotFound, "missing", WithCategory(CategoryDomain))))
	assert.False(t, IsInfraError(New(KindInvalid, ErrValidationFailed, "bad", WithCategory(CategoryValidation))))
	assert.False(t, IsInfraError(New(KindUnauthenticated, ErrAuthUnauthorized, "no", WithCategory(CategoryAuth))))
}

func TestIsDomainNotFound(t *testing.T) {
	domain := New(KindNotFound, ErrSessionNotFound, "missing", WithCategory(CategoryDomain))
	assert.True(t, IsDomainNotFound(domain, ErrSessionNotFound))
	assert.False(t, IsDomainNotFound(domain, ErrOrderNotFound))
	assert.False(t, IsDomainNotFound(New(KindNotFound, ErrSessionNotFound, "missing"), ErrSessionNotFound))
}

func TestIsTransientAndExpected4xx(t *testing.T) {
	// Post-206: transient classification is the WrapInfra marker, not the
	// ErrKeyProviderTransient code string. Constructing the code via New
	// alone no longer makes it transient (downstream Hard).
	sealed := WrapInfra(ErrKeyProviderTransient, "vault sealed", errors.New("sealed"))
	assert.True(t, IsTransient(sealed))
	assert.True(t, IsTransient(fmt.Errorf("wrap: %w", sealed)))
	assert.False(t, IsTransient(New(KindUnavailable, ErrKeyProviderTransient, "vault sealed")))
	assert.False(t, IsTransient(New(KindInternal, ErrKeyProviderEncryptFailed, "encrypt failed")))

	assert.True(t, IsExpected4xx(New(KindInvalid, ErrValidationFailed, "bad")))
	assert.True(t, IsExpected4xx(New(KindUnauthenticated, ErrAuthUnauthorized, "no")))
	assert.False(t, IsExpected4xx(New(KindInternal, ErrInternal, "boom")))
}

func TestPublicCodeForStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   Code
	}{
		{"500 internal", http.StatusInternalServerError, ErrInternal},
		{"501 not implemented → internal", http.StatusNotImplemented, ErrInternal},
		{"502 bad gateway → internal", http.StatusBadGateway, ErrInternal},
		{"503 service unavailable", http.StatusServiceUnavailable, ErrServiceUnavailable},
		{"504 gateway timeout", http.StatusGatewayTimeout, ErrServerTimeout},
		{"507 insufficient storage → internal", http.StatusInsufficientStorage, ErrInternal},
		{"599 unmapped 5xx → internal", 599, ErrInternal},
		{"4xx not handled here → internal sentinel", http.StatusBadRequest, ErrInternal},
		{"0 invalid → internal sentinel", 0, ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PublicCodeForStatus(tc.status))
		})
	}
}

func TestAssertion(t *testing.T) {
	t.Run("noArgs", func(t *testing.T) {
		err := Assertion("plain literal")
		assert.Equal(t, KindInternal, err.Kind)
		assert.Equal(t, ErrInternal, err.Code)
		assert.Equal(t, "plain literal", err.Message)
	})

	t.Run("sprintf", func(t *testing.T) {
		err := Assertion("registry: %s name=%s", "duplicate", "foo")
		assert.Equal(t, "registry: duplicate name=foo", err.Message)
	})

	t.Run("status500", func(t *testing.T) {
		err := Assertion("anything")
		assert.Equal(t, http.StatusInternalServerError, err.Status())
	})

	t.Run("publicCodeIsErrInternal", func(t *testing.T) {
		err := Assertion("anything")
		assert.Equal(t, ErrInternal, err.PublicCode())
	})

	t.Run("errorsAs", func(t *testing.T) {
		err := Assertion("registry: empty")
		var ec *Error
		assert.True(t, errors.As(err, &ec))
		assert.Equal(t, ErrInternal, ec.Code)
	})

	t.Run("panicRecover", func(t *testing.T) {
		defer func() {
			rec := recover()
			require.NotNil(t, rec)
			ec, ok := rec.(*Error)
			require.True(t, ok)
			assert.Equal(t, ErrInternal, ec.Code)
			assert.Contains(t, ec.Message, "registry: oops")
		}()
		panic(Assertion("registry: %v", errors.New("oops")))
	})

	t.Run("noCause", func(t *testing.T) {
		err := Assertion("no cause attached")
		assert.Nil(t, err.Cause)
	})

	t.Run("isInfraErrorTrue", func(t *testing.T) {
		err := Assertion("infra path")
		assert.True(t, IsInfraError(err))
		assert.Equal(t, CategoryInfra, err.Category)
	})
}

func TestWithDetailsAttrs(t *testing.T) {
	t.Run("singleAttr", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad", WithDetails(PublicString("field", "name")))
		require.Len(t, err.Details, 1)
		assert.Equal(t, "field", err.Details[0].Key())
		assert.Equal(t, "name", err.Details[0].Value())
	})

	t.Run("multiOrdering", func(t *testing.T) {
		details := []PublicDetail{
			PublicString("a", "1"),
			PublicInt("b", 2),
			PublicBool("c", true),
		}
		err := New(KindInvalid, ErrValidationFailed, "bad", WithDetails(details...))
		require.Len(t, err.Details, 3)
		assert.Equal(t, "a", err.Details[0].Key())
		assert.Equal(t, "b", err.Details[1].Key())
		assert.Equal(t, "c", err.Details[2].Key())
	})

	t.Run("empty", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad", WithDetails())
		assert.Nil(t, err.Details)
	})

	t.Run("appendCumulative", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(PublicString("a", "1")),
			WithDetails(PublicString("b", "2")),
		)
		require.Len(t, err.Details, 2)
		assert.Equal(t, "a", err.Details[0].Key())
		assert.Equal(t, "b", err.Details[1].Key())
	})

	t.Run("findAttrHit", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(PublicString("reason", "expired")),
		)
		d, ok := err.FindAttr("reason")
		require.True(t, ok)
		assert.Equal(t, "expired", d.Value())
	})

	t.Run("findAttrMiss", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(PublicString("a", "1")),
		)
		_, ok := err.FindAttr("missing")
		assert.False(t, ok)
	})

	t.Run("findAttrNilDetails", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad")
		_, ok := err.FindAttr("anything")
		assert.False(t, ok)
	})
}

func TestErrorMarshalJSON(t *testing.T) {
	t.Run("publicErrorLiteralUsesEmptyDetailsArray", func(t *testing.T) {
		raw, mErr := json.Marshal(PublicError{
			Code:    ErrInternal,
			Message: "internal server error",
		})
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, []any{}, got["details"])
	})

	t.Run("publicErrorLiteralDropsZeroValueDetails", func(t *testing.T) {
		raw, mErr := json.Marshal(PublicError{
			Code:    ErrValidationFailed,
			Message: "bad",
			Details: []PublicDetail{
				{},
				PublicString("field", "name"),
			},
		})
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, []any{
			map[string]any{"key": "field", "value": "name"},
		}, got["details"])
		assert.NotContains(t, string(raw), "null")
	})

	t.Run("noDetailsClient", func(t *testing.T) {
		err := New(KindNotFound, ErrCellNotFound, "cell not found")
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, "ERR_CELL_NOT_FOUND", got["code"])
		assert.Equal(t, "cell not found", got["message"])
		assert.Equal(t, []any{}, got["details"])
	})

	t.Run("singleAttrClient", func(t *testing.T) {
		err := New(KindNotFound, ErrCellNotFound, "cell not found",
			WithDetails(PublicString("cellId", "abc")))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, []any{
			map[string]any{"key": "cellId", "value": "abc"},
		}, got["details"])
	})

	t.Run("directDetailsMutationDropsZeroValueDetails", func(t *testing.T) {
		err := New(KindNotFound, ErrCellNotFound, "cell not found")
		err.Details = []PublicDetail{{}, PublicString("cellId", "abc")}
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, []any{
			map[string]any{"key": "cellId", "value": "abc"},
		}, got["details"])
		assert.NotContains(t, string(raw), "null")
	})

	t.Run("multiAttrClient", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(
				PublicString("field", "name"),
				PublicInt("len", 0),
				PublicBool("required", true),
			))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		details, ok := got["details"].([]any)
		require.True(t, ok)
		require.Len(t, details, 3)
		assert.Equal(t, "field", details[0].(map[string]any)["key"])
		assert.Equal(t, "name", details[0].(map[string]any)["value"])
		assert.Equal(t, "len", details[1].(map[string]any)["key"])
		assert.Equal(t, float64(0), details[1].(map[string]any)["value"])
		assert.Equal(t, "required", details[2].(map[string]any)["key"])
		assert.Equal(t, true, details[2].(map[string]any)["value"])
	})

	t.Run("internalNotMarshaled", func(t *testing.T) {
		err := New(KindNotFound, ErrCellNotFound, "cell not found",
			WithInternal(InternalAttr("_", "internal trace data")))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		assert.NotContains(t, string(raw), "internal trace data")
		assert.NotContains(t, string(raw), "internalDetails")
	})

	t.Run("serverErrStripsDetails", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom",
			WithDetails(PublicString("dsn", "secret"), PublicInt("retries", 3)))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, "ERR_INTERNAL", got["code"])
		assert.Equal(t, "internal server error", got["message"])
		assert.Equal(t, []any{}, got["details"])
		// Server-side details must not leak even by accident.
		assert.NotContains(t, string(raw), "dsn")
		assert.NotContains(t, string(raw), "secret")
		assert.NotContains(t, string(raw), "boom")
	})
}

func TestRenderPublic(t *testing.T) {
	t.Run("directErrcode", func(t *testing.T) {
		cause := errors.New("postgres://user:secret@example/db")
		err := Wrap(
			KindNotFound,
			ErrZeroTestMatch,
			"pattern matched no tests — check your YAML ref",
			cause,
			WithInternal(InternalAttr("_", `pattern="TestSecret" pkg=./cells token=hunter2`)),
			WithDetails(PublicString("ref", "journey.J-login.auto")),
		)

		got := RenderPublic(err)
		assert.Contains(t, got, "ERR_ZERO_TEST_MATCH")
		assert.Contains(t, got, "pattern matched no tests — check your YAML ref")
		assert.Contains(t, got, `ref="journey.J-login.auto"`)
		assert.NotContains(t, got, "TestSecret")
		assert.NotContains(t, got, "hunter2")
		assert.NotContains(t, got, "postgres://")
	})

	t.Run("wrappedErrcodeKeepsOuterContext", func(t *testing.T) {
		inner := New(
			KindNotFound,
			ErrZeroTestMatch,
			"pattern matched no tests — check your YAML ref",
			WithInternal(InternalAttr("_", `pattern="TestSecret" pkg=./cells token=hunter2`)),
		)

		got := RenderPublic(fmt.Errorf("verify journey --active: %w", inner))
		assert.Contains(t, got, "verify journey --active")
		assert.Contains(t, got, "ERR_ZERO_TEST_MATCH")
		assert.Contains(t, got, "pattern matched no tests — check your YAML ref")
		assert.NotContains(t, got, "TestSecret")
		assert.NotContains(t, got, "hunter2")
	})

	t.Run("serverErrcodeUsesStatusLevelPublicSurface", func(t *testing.T) {
		err := New(
			KindInternal,
			ErrAuthRoleFetchFailed,
			"role repository failed: postgres://user:secret@example/db",
			WithInternal(InternalAttr("_", "token=hunter2")),
		)

		got := RenderPublic(err)
		assert.Equal(t, "[ERR_INTERNAL] internal server error", got)
		assert.NotContains(t, got, "ERR_AUTH_ROLE_FETCH_FAILED")
		assert.NotContains(t, got, "postgres://")
		assert.NotContains(t, got, "hunter2")
	})

	t.Run("operatorStringKeepsSafeRoutingMetadataForServerErrcode", func(t *testing.T) {
		err := New(
			KindInternal,
			ErrAuthRoleFetchFailed,
			"role repository failed: postgres://user:secret@example/db",
			WithInternal(InternalAttr("_", "token=hunter2")),
		)

		got := OperatorString(err)
		assert.Equal(t, "[ERR_INTERNAL] internal server error (status=500, sourceCode=ERR_AUTH_ROLE_FETCH_FAILED)", got)
		assert.NotContains(t, got, "postgres://")
		assert.NotContains(t, got, "hunter2")
	})

	t.Run("joinedErrcodesAllUsePublicSurface", func(t *testing.T) {
		first := New(
			KindInvalid,
			ErrValidationFailed,
			"invalid config",
			WithInternal(InternalAttr("_", "token=first-secret")),
			WithDetails(PublicString("field", "cell.id")),
		)
		second := Wrap(
			KindInternal,
			ErrAuthRoleFetchFailed,
			"role lookup failed: dsn=secret",
			errors.New("cause=second-secret"),
			WithInternal(InternalAttr("_", "token=second-secret")),
		)

		got := RenderPublic(fmt.Errorf("verify generated: %w", errors.Join(first, second)))
		assert.Contains(t, got, "verify generated:")
		assert.Contains(t, got, "[ERR_VALIDATION_FAILED] invalid config")
		assert.Contains(t, got, `field="cell.id"`)
		assert.Contains(t, got, "[ERR_INTERNAL] internal server error")
		for _, leak := range []string{
			"first-secret",
			"second-secret",
			"ERR_AUTH_ROLE_FETCH_FAILED",
			"dsn=secret",
			"cause=second-secret",
		} {
			assert.NotContains(t, got, leak)
		}
	})

	t.Run("stringDetailsAreQuoted", func(t *testing.T) {
		err := New(
			KindInvalid,
			ErrValidationFailed,
			"invalid config",
			WithDetails(PublicString("field", "cell.id,owner\nname"), PublicInt("limit", 2)),
		)

		got := RenderPublic(err)
		assert.Contains(t, got, `field="cell.id,owner\nname"`)
		assert.Contains(t, got, "limit=2")
	})

	t.Run("detailsUseJSONWireFormatting", func(t *testing.T) {
		at := time.Date(2026, 5, 6, 1, 2, 3, 0, time.UTC)
		err := New(
			KindInvalid,
			ErrValidationFailed,
			"invalid config",
			WithDetails(PublicDuration("timeout", time.Second), PublicTime("at", at)),
		)

		got := RenderPublic(err)
		assert.Contains(t, got, "timeout=1000000000")
		assert.Contains(t, got, `at="2026-05-06T01:02:03Z"`)
	})
}

func TestOperatorProjection(t *testing.T) {
	first := New(
		KindInvalid,
		ErrValidationFailed,
		"invalid config",
		WithDetails(PublicString("field", "cell.id")),
	)
	second := New(
		KindInternal,
		ErrAuthRoleFetchFailed,
		"role lookup failed: dsn=secret",
		WithInternal(InternalAttr("_", "token=secret")),
	)

	got := OperatorProjection(fmt.Errorf("verify generated: %w", errors.Join(first, second)))
	require.Len(t, got, 2)
	require.Len(t, got[0].Details, 1)
	assert.Equal(t, ErrValidationFailed, got[0].Code)
	assert.Equal(t, "invalid config", got[0].Message)
	assert.Equal(t, "field", got[0].Details[0].Key())
	assert.Equal(t, "cell.id", got[0].Details[0].Value())
	assert.Equal(t, PublicError{
		Code:       ErrInternal,
		Message:    "internal server error",
		Details:    []PublicDetail{},
		SourceCode: ErrAuthRoleFetchFailed,
		Status:     http.StatusInternalServerError,
	}, got[1])
}

func TestProjectionFallbacksAndMethodStrings(t *testing.T) {
	assert.Nil(t, PublicProjection(nil))
	assert.Nil(t, OperatorProjection(nil))
	assert.Empty(t, RenderPublic(nil))
	assert.Empty(t, OperatorString(nil))

	publicPlain := PublicProjection(errors.New("dsn=postgres://user:secret@example/db"))
	require.Len(t, publicPlain, 1)
	assert.Equal(t, PublicError{
		Code:    ErrInternal,
		Message: "internal server error",
		Details: []PublicDetail{},
	}, publicPlain[0])

	operatorPlain := OperatorProjection(errors.New("plain failure"))
	require.Len(t, operatorPlain, 1)
	assert.Equal(t, PublicError{
		Code:    ErrInternal,
		Message: "plain failure",
		Details: []PublicDetail{},
	}, operatorPlain[0])

	matched := New(
		KindInvalid,
		ErrValidationFailed,
		"invalid config",
		WithDetails(PublicString("field", "cell.id")),
	)
	matchedProjection := PublicProjection(errcodeAsError{target: matched})
	require.Len(t, matchedProjection, 1)
	require.Len(t, matchedProjection[0].Details, 1)
	assert.Equal(t, ErrValidationFailed, matchedProjection[0].Code)
	assert.Equal(t, "invalid config", matchedProjection[0].Message)
	assert.Equal(t, "field", matchedProjection[0].Details[0].Key())
	assert.Equal(t, "cell.id", matchedProjection[0].Details[0].Value())

	server := New(
		KindInternal,
		ErrAuthRoleFetchFailed,
		"role lookup failed: dsn=secret",
		WithInternal(InternalAttr("_", "token=secret")),
	)
	assert.Equal(t, "[ERR_INTERNAL] internal server error", server.PublicString())
	assert.Equal(
		t,
		"[ERR_INTERNAL] internal server error (status=500, sourceCode=ERR_AUTH_ROLE_FETCH_FAILED)",
		server.OperatorString(),
	)

	var nilErr *Error
	assert.Empty(t, nilErr.PublicString())
	assert.Empty(t, nilErr.OperatorString())
}
