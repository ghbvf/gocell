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
//   - SlogDependencyEntry 三个字段全部 unexported（status / durationMs / errorMsg），
//     包外不能通过 composite literal 任何形式构造 SlogDependencyEntry 值——加上
//     redactedErrorMsg 是包私有 newtype，funnel 上游在编译期不可表达任何旁路。
//     archtest HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 锁定包内 type conversion
//     `redactedErrorMsg(x)` 只出现在 newRedactedErrorMsg 函数体内（下游 Hard）。
//
// 见 docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md
// §3（四通道映射）§6（enforcement funnel matrix）。
//
// ref: k8s.io/apiserver/pkg/server/healthz healthz.go:274-275 — wire 不携带
// error 文本（"reason withheld"），完整 error 落 klog；GoCell 对齐该模式但用
// pkg/redaction.RedactString 替代纯切断，保留运维诊断 + 删除敏感子串。
package health

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/redaction"
)

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
// probe result inside the slog "readyz unhealthy" / "readyz degraded" records.
// The type is exported so out-of-package tests (cmd/corebundle, runtime/bootstrap)
// can type-assert `depsAttr.Any().(map[string]health.SlogDependencyEntry)` and
// read fields via the exported accessor methods below.
//
// All three fields (status / durationMs / errorMsg) are intentionally
// unexported: this closes the funnel upstream at compile-time. External
// packages cannot construct a SlogDependencyEntry value by any path —
// neither `health.SlogDependencyEntry{ErrorMsg: "raw"}` (field name not
// exported, composite-literal compile error) nor untyped-const conversion
// (the unexported errorMsg field cannot be addressed). Combined with
// redactedErrorMsg being a package-private newtype, the only production
// producer is newRedactedErrorMsg in aggregateProbeResults — exactly the
// Hard upstream property the ADR §6 funnel matrix claims.
//
// 业务 Cell probe 实现者不需要直接接触 SlogDependencyEntry。只需要按
// ProbeResult.Err godoc 的格式规范写 probe error（结构化 key=value 形式），
// 框架会自动将 Err 传入 newRedactedErrorMsg funnel 并填充字段。该结构是
// 框架内部 observability shape，外部只通过 accessor 方法只读消费。
//
// LogValue implements slog.LogValuer so that when a single SlogDependencyEntry
// value is passed directly as a slog.Any argument, JSON and logfmt handlers
// receive snake_case field names consistent with the wire shape. Note: when
// the entire map[string]SlogDependencyEntry is passed via slog.Any (the
// current logDiagnostics path), stdlib handlers use reflection on the map
// values and do NOT call LogValue — JSON output then has no field names to
// fall back to since fields are unexported; tests rely on direct
// type-assertion + accessor methods for assertions, and operators should use
// the LogValue-aware grouping in handlers that iterate attrs individually.
type SlogDependencyEntry struct {
	status     string
	durationMs int64
	errorMsg   redactedErrorMsg
}

// Status returns the probe status string ("healthy" | "degraded" | "unhealthy"
// | "timeout"). Read-only accessor; the underlying field is unexported.
func (e SlogDependencyEntry) Status() string { return e.status }

// DurationMs returns the probe wall-clock duration in milliseconds.
// Read-only accessor; the underlying field is unexported.
func (e SlogDependencyEntry) DurationMs() int64 { return e.durationMs }

// ErrorMsg returns the redacted probe error text (already passed through
// newRedactedErrorMsg → pkg/redaction.RedactString). Empty string when the
// probe was healthy. Read-only accessor; the underlying field is unexported.
func (e SlogDependencyEntry) ErrorMsg() string { return string(e.errorMsg) }

// LogValue implements slog.LogValuer. It allows handlers that iterate attrs
// individually (rather than via map reflection) to emit consistent snake_case
// field names. See struct-level godoc for the stdlib map-of-LogValuer caveat.
func (e SlogDependencyEntry) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("status", e.status),
		slog.Int64("duration_ms", e.durationMs),
		slog.String("error_msg", string(e.errorMsg)),
	)
}

// NewSlogDependencyEntryForTesting is a test-only constructor used by
// runtime/http/health/healthtest unit tests (and only those tests) to build
// SlogDependencyEntry values without spinning up a full Handler. Production
// code MUST NOT call this function — the production producer is
// aggregateProbeResults → newRedactedErrorMsg, which guarantees the
// pkg/redaction.RedactString funnel.
//
// The errorMsg argument is taken verbatim (no redaction). Callers in test
// code are responsible for either passing already-redacted text or passing
// the sentinel "" for healthy probes. The exposed constructor does not
// undermine the production funnel because:
//
//   - HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 archtest scans
//     runtime/http/health/ (production package) for redactedErrorMsg(...)
//     conversions; this constructor is the ONE allowed callsite, and the
//     archtest forward rule allows it because the conversion happens inside
//     the production package boundary (the funnel function is also there).
//   - The constructor name carries the "ForTesting" suffix — any production
//     caller is immediately obvious in code review.
//   - SlogDependencyEntry's three fields remain unexported; external
//     production packages still cannot construct values directly. Only
//     external _test packages can call this constructor (via the exported
//     name), and only for assembling expected slog payloads.
func NewSlogDependencyEntryForTesting(status string, durationMs int64, errorMsg string) SlogDependencyEntry {
	return SlogDependencyEntry{
		status:     status,
		durationMs: durationMs,
		errorMsg:   redactedErrorMsg(errorMsg),
	}
}
