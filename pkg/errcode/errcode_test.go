package errcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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

func TestNewWrapAndOptions(t *testing.T) {
	cause := errors.New("pool exhausted")
	err := Wrap(
		KindUnavailable,
		ErrServiceUnavailable,
		"service unavailable",
		cause,
		WithInternal("postgres pool exhausted"),
		WithDetails(slog.Bool("retry", true)),
		WithCategory(CategoryInfra),
	)

	assert.Equal(t, KindUnavailable, err.Kind)
	assert.Equal(t, ErrServiceUnavailable, err.Code)
	assert.Equal(t, "service unavailable", err.Message)
	assert.Equal(t, "postgres pool exhausted", err.InternalMessage)
	assert.Equal(t, []slog.Attr{slog.Bool("retry", true)}, err.Details)
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
	assert.True(t, IsTransient(New(KindUnavailable, ErrKeyProviderTransient, "vault sealed")))
	assert.True(t, IsTransient(fmt.Errorf("wrap: %w", New(KindUnavailable, ErrKeyProviderTransient, "vault sealed"))))
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
		err := New(KindInvalid, ErrValidationFailed, "bad", WithDetails(slog.String("field", "name")))
		assert.Equal(t, []slog.Attr{slog.String("field", "name")}, err.Details)
	})

	t.Run("multiOrdering", func(t *testing.T) {
		attrs := []slog.Attr{
			slog.String("a", "1"),
			slog.Int("b", 2),
			slog.Bool("c", true),
		}
		err := New(KindInvalid, ErrValidationFailed, "bad", WithDetails(attrs...))
		require.Len(t, err.Details, 3)
		assert.Equal(t, "a", err.Details[0].Key)
		assert.Equal(t, "b", err.Details[1].Key)
		assert.Equal(t, "c", err.Details[2].Key)
	})

	t.Run("empty", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad", WithDetails())
		assert.Nil(t, err.Details)
	})

	t.Run("appendCumulative", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(slog.String("a", "1")),
			WithDetails(slog.String("b", "2")),
		)
		require.Len(t, err.Details, 2)
		assert.Equal(t, "a", err.Details[0].Key)
		assert.Equal(t, "b", err.Details[1].Key)
	})

	t.Run("findAttrHit", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(slog.String("reason", "expired")),
		)
		attr, ok := err.FindAttr("reason")
		require.True(t, ok)
		assert.Equal(t, "expired", attr.Value.String())
	})

	t.Run("findAttrMiss", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(slog.String("a", "1")),
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
			WithDetails(slog.String("cellId", "abc")))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, []any{
			map[string]any{"key": "cellId", "value": "abc"},
		}, got["details"])
	})

	t.Run("multiAttrClient", func(t *testing.T) {
		err := New(KindInvalid, ErrValidationFailed, "bad",
			WithDetails(
				slog.String("field", "name"),
				slog.Int("len", 0),
				slog.Bool("required", true),
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
			WithInternal("internal trace data"))
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		assert.NotContains(t, string(raw), "internal trace data")
		assert.NotContains(t, string(raw), "internalMessage")
	})

	t.Run("serverErrStripsDetails", func(t *testing.T) {
		err := New(KindInternal, ErrInternal, "boom",
			WithDetails(slog.String("dsn", "secret"), slog.Int("retries", 3)))
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

	t.Run("clientErrSubstitutesUnsafeKindBypassingEntry", func(t *testing.T) {
		// Construct an *Error directly so the wire-unsafe attr bypasses the
		// WithDetails kind whitelist. MarshalJSON must substitute the
		// unsafeKindMarker rather than emit a handler-dependent payload.
		err := &Error{
			Kind:    KindInvalid,
			Code:    ErrValidationFailed,
			Message: "bad",
			Details: []slog.Attr{
				slog.String("ok", "v"),
				slog.Any("ch", make(chan int)), // KindAny — not wire-safe
			},
		}
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		details, ok := got["details"].([]any)
		require.True(t, ok)
		require.Len(t, details, 2)
		assert.Equal(t, "v", details[0].(map[string]any)["value"])
		assert.Equal(t, unsafeKindMarker, details[1].(map[string]any)["value"],
			"wire-unsafe kind must be replaced by sentinel string")
	})

	t.Run("clientErrSubstitutesNonFiniteFloatBypassingEntry", func(t *testing.T) {
		err := &Error{
			Kind:    KindInvalid,
			Code:    ErrValidationFailed,
			Message: "bad",
			Details: []slog.Attr{
				slog.Float64("ratio", math.Inf(1)),
			},
		}
		raw, mErr := json.Marshal(err)
		require.NoError(t, mErr)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		details, ok := got["details"].([]any)
		require.True(t, ok)
		require.Len(t, details, 1)
		assert.Equal(t, unsafeValueMarker, details[0].(map[string]any)["value"],
			"non-finite float must be replaced by sentinel string")
	})
}

func TestWithDetailsKindWhitelist(t *testing.T) {
	t.Run("scalarKindsAccepted", func(t *testing.T) {
		require.NotPanics(t, func() {
			_ = New(KindInvalid, ErrValidationFailed, "ok", WithDetails(
				slog.String("s", "v"),
				slog.Int("i", 1),
				slog.Int64("i64", 1),
				slog.Uint64("u64", 1),
				slog.Float64("f", 1),
				slog.Bool("b", true),
				slog.Duration("d", 0),
				slog.Time("t", time.Time{}),
			))
		})
	})

	t.Run("anyKindPanics", func(t *testing.T) {
		var ec *Error
		defer func() {
			r := recover()
			require.NotNil(t, r, "WithDetails(slog.Any(...)) must panic")
			err, ok := r.(error)
			require.True(t, ok, "panic value must be error from Assertion()")
			require.True(t, errors.As(err, &ec), "panic must wrap *errcode.Error")
			assert.Contains(t, ec.Message, "wire-unsafe kind")
			assert.Contains(t, ec.Message, "Any")
		}()
		_ = New(KindInvalid, ErrValidationFailed, "ok",
			WithDetails(slog.Any("x", struct{ A int }{42})))
	})

	t.Run("groupKindPanics", func(t *testing.T) {
		require.Panics(t, func() {
			_ = New(KindInvalid, ErrValidationFailed, "ok",
				WithDetails(slog.Group("g", slog.String("inner", "v"))))
		})
	})

	t.Run("nonFiniteFloatPanics", func(t *testing.T) {
		cases := []struct {
			name string
			val  float64
		}{
			{name: "nan", val: math.NaN()},
			{name: "positive_inf", val: math.Inf(1)},
			{name: "negative_inf", val: math.Inf(-1)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var ec *Error
				defer func() {
					r := recover()
					require.NotNil(t, r, "WithDetails(slog.Float64(non-finite)) must panic")
					err, ok := r.(error)
					require.True(t, ok, "panic value must be error from Assertion()")
					require.True(t, errors.As(err, &ec), "panic must wrap *errcode.Error")
					assert.Contains(t, ec.Message, "non-finite float64")
				}()
				_ = New(KindInvalid, ErrValidationFailed, "ok",
					WithDetails(slog.Float64("ratio", tc.val)))
			})
		}
	})
}

func TestPublicString(t *testing.T) {
	t.Run("directErrcode", func(t *testing.T) {
		cause := errors.New("postgres://user:secret@example/db")
		err := Wrap(
			KindNotFound,
			ErrZeroTestMatch,
			"pattern matched no tests — check your YAML ref",
			cause,
			WithInternal(`pattern="TestSecret" pkg=./cells token=hunter2`),
			WithDetails(slog.String("ref", "journey.J-login.auto")),
		)

		got := PublicString(err)
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
			WithInternal(`pattern="TestSecret" pkg=./cells token=hunter2`),
		)

		got := PublicString(fmt.Errorf("verify journey --active: %w", inner))
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
			WithInternal("token=hunter2"),
		)

		got := PublicString(err)
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
			WithInternal("token=hunter2"),
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
			WithInternal("token=first-secret"),
			WithDetails(slog.String("field", "cell.id")),
		)
		second := Wrap(
			KindInternal,
			ErrAuthRoleFetchFailed,
			"role lookup failed: dsn=secret",
			errors.New("cause=second-secret"),
			WithInternal("token=second-secret"),
		)

		got := PublicString(fmt.Errorf("verify generated: %w", errors.Join(first, second)))
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
			WithDetails(slog.String("field", "cell.id,owner\nname"), slog.Int("limit", 2)),
		)

		got := PublicString(err)
		assert.Contains(t, got, `field="cell.id,owner\nname"`)
		assert.Contains(t, got, "limit=2")
	})

	t.Run("detailsUseJSONWireFormatting", func(t *testing.T) {
		at := time.Date(2026, 5, 6, 1, 2, 3, 0, time.UTC)
		err := New(
			KindInvalid,
			ErrValidationFailed,
			"invalid config",
			WithDetails(slog.Duration("timeout", time.Second), slog.Time("at", at)),
		)

		got := PublicString(err)
		assert.Contains(t, got, "timeout=1000000000")
		assert.Contains(t, got, `at="2026-05-06T01:02:03Z"`)
	})
}

func TestOperatorProjection(t *testing.T) {
	first := New(
		KindInvalid,
		ErrValidationFailed,
		"invalid config",
		WithDetails(slog.String("field", "cell.id")),
	)
	second := New(
		KindInternal,
		ErrAuthRoleFetchFailed,
		"role lookup failed: dsn=secret",
		WithInternal("token=secret"),
	)

	got := OperatorProjection(fmt.Errorf("verify generated: %w", errors.Join(first, second)))
	require.Len(t, got, 2)
	assert.Equal(t, PublicError{
		Code:    ErrValidationFailed,
		Message: "invalid config",
		Details: []PublicDetail{
			{Key: "field", Value: "cell.id"},
		},
	}, got[0])
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
	assert.Empty(t, PublicString(nil))
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
		WithDetails(slog.String("field", "cell.id")),
	)
	matchedProjection := PublicProjection(errcodeAsError{target: matched})
	require.Len(t, matchedProjection, 1)
	assert.Equal(t, PublicError{
		Code:    ErrValidationFailed,
		Message: "invalid config",
		Details: []PublicDetail{
			{Key: "field", Value: "cell.id"},
		},
	}, matchedProjection[0])

	server := New(
		KindInternal,
		ErrAuthRoleFetchFailed,
		"role lookup failed: dsn=secret",
		WithInternal("token=secret"),
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
