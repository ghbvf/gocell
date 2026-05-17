// verbose_shape.go — typed wire/slog shapes for /readyz?verbose paths.
//
// INVARIANT: HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01
// INVARIANT: HEALTH-REDACTED-ERROR-MSG-FUNNEL-01
//
// 这两个 InvariantID 把"/readyz?verbose 503 body 不携带 error 文本"和"slog
// dependencies entry 的 error 文本必经 pkg/redaction.RedactString"两个安全
// 不变量翻译成 type system + archtest 形态：
//
//   - verboseDependencyEntry 的字段集冻结（status / duration_ms），加字段需先
//     扩 archtest allowlist 才能 CI 绿；error 文本属于第 d 通道（ops-diagnostics
//     slog），不上 wire。
//   - redactedErrorMsg 是包私有 newtype；包外不可表达任何形式的构造，包内唯一
//     生产构造函数 newRedactedErrorMsg 强制经过 pkg/redaction.RedactString。
//     archtest HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 锁定包内 type conversion
//     `redactedErrorMsg(x)` 只出现在 newRedactedErrorMsg 函数体内。
//
// 见 docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md
// §3（四通道映射）§6（enforcement funnel matrix）。
//
// ref: k8s.io/apiserver/pkg/server/healthz healthz.go:274-275 — wire 不携带
// error 文本（"reason withheld"），完整 error 落 klog；GoCell 对齐该模式但用
// pkg/redaction.RedactString 替代纯切断，保留运维诊断 + 删除敏感子串。
package health

import "github.com/ghbvf/gocell/pkg/redaction"

// verboseDependencyEntry is the wire shape for /readyz?verbose body
// dependencies entries. Field set is intentionally minimal: status + duration
// only. Error text MUST NOT appear here — it belongs to channel d
// (ops-diagnostics slog) per the four-channel redaction model
// (docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md).
//
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 archtest enforces field set equality;
// any field addition fails CI and requires updating both the allowlist and
// the ADR threat matrix.
type verboseDependencyEntry struct {
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
}

// redactedErrorMsg carries an error text that has already been passed through
// pkg/redaction.RedactString. Production construction is restricted to
// newRedactedErrorMsg by HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 archtest: the
// only type-conversion CallExpr to redactedErrorMsg in runtime/http/health/
// non-test code is the one inside newRedactedErrorMsg.
//
// The type is unexported, so packages outside runtime/http/health cannot
// construct redactedErrorMsg values at all — the funnel is closed both
// upstream (package boundary) and downstream (archtest).
type redactedErrorMsg string

// newRedactedErrorMsg is the sole production producer of redactedErrorMsg
// values. nil input → "" sentinel (no mask on empty); non-nil err.Error()
// passes through pkg/redaction.RedactString to mask sensitive substrings
// (password / token / DSN / Authorization headers / etc., see
// pkg/redaction.sensitiveKeyPattern for the full key list).
func newRedactedErrorMsg(err error) redactedErrorMsg {
	if err == nil {
		return ""
	}
	return redactedErrorMsg(redaction.RedactString(err.Error()))
}

// SlogDependencyEntry is the ops-diagnostics shape (channel d) for a single
// probe result inside the slog "readyz unhealthy" record. It is exported so
// out-of-package tests (cmd/corebundle, runtime/bootstrap) can type-assert
// `depsAttr.Any().(map[string]health.SlogDependencyEntry)`. The struct is
// emitted under slog.Any("dependencies", map[string]SlogDependencyEntry{...})
// in (*readyzResult).logUnhealthy.
//
// ErrorMsg is typed redactedErrorMsg (unexported) so the type system
// guarantees every value has already passed through the newRedactedErrorMsg
// funnel — pkg/redaction.RedactString — before reaching slog backends.
// External readers may convert to string via `string(e.ErrorMsg)` (the
// conversion expression names string, not the unexported source type, so it
// compiles cleanly from any package).
//
// JSON tags enable slog.JSONHandler to serialize snake_case keys consistent
// with the wire shape; other log handlers (logfmt, custom) fall back to
// reflected field names.
type SlogDependencyEntry struct {
	Status     string           `json:"status"`
	DurationMs int64            `json:"duration_ms"`
	ErrorMsg   redactedErrorMsg `json:"error_msg,omitempty"`
}
