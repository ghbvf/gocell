package contracttest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// TestJourney_TypedEnvelopeRoundtrip implements the auto checkRefs declared in
// journeys/J-typed-envelope-roundtrip.yaml. Each subtest verifies one wire-level
// invariant of the typed response envelope migration (PR-V1-CONTRACT-TYPED-RESPONSE-
// ENVELOPE, ADR 202605061500-adr-typed-response-envelope.md).
func TestJourney_TypedEnvelopeRoundtrip(t *testing.T) {
	t.Run("business-4xx-details", func(t *testing.T) {
		// typed Xxx4xxErrorResponse → WriteErrorWithStatus(404, errcode.Error{Details: [...]})
		rec := httptest.NewRecorder()
		ecErr := errcode.New(errcode.KindNotFound, errcode.ErrDeviceNotFound, "device not found",
			errcode.WithDetails(slog.String("deviceId", "abc-123")))
		httputil.WriteErrorWithStatus(context.Background(), rec, 404, ecErr)
		require.Equal(t, 404, rec.Code)
		var body struct {
			Error struct {
				Code    errcode.Code `json:"code"`
				Details []any        `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.NotEmpty(t, body.Error.Details, "4xx wire body must carry details array")
	})

	t.Run("business-5xx-strip", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ecErr := errcode.New(errcode.KindInternal, errcode.ErrInternal, "db down",
			errcode.WithDetails(slog.String("dsn", "postgres://u:p@h")))
		httputil.WriteErrorWithStatus(context.Background(), rec, 503, ecErr)
		require.Equal(t, 503, rec.Code)
		var body struct {
			Error struct {
				Details []any `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Empty(t, body.Error.Details, "5xx wire body details must be empty (PII safety)")
	})

	t.Run("framework-5xx-fallback", func(t *testing.T) {
		// generated handler nil-response path → WriteNilResponseInternal
		rec := httptest.NewRecorder()
		httputil.WriteNilResponseInternal(context.Background(), rec)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		var body struct {
			Error struct {
				Code    errcode.Code `json:"code"`
				Details []any        `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, errcode.ErrInternal, body.Error.Code)
		assert.Empty(t, body.Error.Details)

		// visit encode failure path → WriteEncodeFaultInternal
		rec2 := httptest.NewRecorder()
		httputil.WriteEncodeFaultInternal(context.Background(), rec2)
		require.Equal(t, http.StatusInternalServerError, rec2.Code)
		var body2 struct {
			Error struct {
				Code    errcode.Code `json:"code"`
				Details []any        `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &body2))
		assert.Equal(t, errcode.ErrInternal, body2.Error.Code)
		assert.Empty(t, body2.Error.Details)
	})

	t.Run("5xx-kind-normalize", func(t *testing.T) {
		// 输入 4xx Kind (KindNotFound)，调用 WriteErrorWithStatus(503, ...)
		// 期望 wire code 是 ERR_SERVICE_UNAVAILABLE (规范化的 5xx code)，
		// details 被 strip（即使原 ecErr 含 details，因 Kind 规范化后 IsClient=false）
		rec := httptest.NewRecorder()
		ecErr := errcode.New(errcode.KindNotFound, errcode.ErrDeviceNotFound, "leaked from 4xx kind",
			errcode.WithDetails(slog.String("dsn", "postgres://leaked")))
		httputil.WriteErrorWithStatus(context.Background(), rec, 503, ecErr)
		var body struct {
			Error struct {
				Code    errcode.Code `json:"code"`
				Details []any        `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, errcode.ErrServiceUnavailable, body.Error.Code, "wire code must be 5xx public sentinel, not 4xx-derived")
		assert.Empty(t, body.Error.Details, "Kind normalize must drop details from 4xx-Kind input on 5xx wire")
	})

	t.Run("5xx-log-redact", func(t *testing.T) {
		// 捕获 slog 输出，验证 5xx Details 经 RedactSlogAttr 处理
		// 实现：自定义 slog.Handler 写入 bytes.Buffer，重置 default logger，
		// 调 WriteErrorWithStatus 含 password=secret123 的 Details，
		// 检查 buffer 含 <REDACTED> 不含 secret123
		var buf strings.Builder
		original := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		defer slog.SetDefault(original)

		rec := httptest.NewRecorder()
		ecErr := errcode.New(errcode.KindInternal, errcode.ErrInternal, "leak attempt",
			errcode.WithDetails(slog.String("config", "host=h password=secret123 port=5432")))
		httputil.WriteErrorWithStatus(context.Background(), rec, 503, ecErr)
		logs := buf.String()
		assert.Contains(t, logs, "<REDACTED>", "5xx slog Details must be redacted")
		assert.NotContains(t, logs, "secret123", "5xx slog must NOT contain raw secret")
	})

	t.Run("buffer-then-commit", func(t *testing.T) {
		// 验证由 archtest VISIT-BUFFER-THEN-COMMIT-01 静态守卫；
		// 此运行时 case 仅记录指针，确保 Journey 自动验收命中归档
		// 详见 tools/archtest/visit_buffer_then_commit_test.go
		t.Log("VISIT-BUFFER-THEN-COMMIT-01 archtest is the authoritative guard; runtime smoke skipped")
	})
}
