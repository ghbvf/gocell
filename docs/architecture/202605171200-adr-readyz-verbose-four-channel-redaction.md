# ADR: Readyz Verbose 四通道分明 — wire 不带 error 文本 + slog 走 typed funnel

- Status: Accepted
- Date: 2026-05-17
- Tracks: PR391-HEALTH-VERBOSE-REDACTION-01（backlog.md cap-04，P1/Cx2 🟠 发布前安全收口）
- Builds on: ADR `202605051730-adr-errcode-message-pii-safety.md`（errcode 三层 redaction：Message / Details / Internal）；PR #391 review security finding
- Implemented by: PR #fix/222-pr391-health-verbose-redaction

## §1 背景

### 现状（PR391 review 时）

`/readyz?verbose=true` 503 响应体的 `dependencies[*]` 数组元素历史上长这样：

```jsonc
{"status": "unhealthy", "duration_ms": 12, "error": "<redacted truncated text>"}
```

源码位置 `runtime/http/health/health.go:426`（PR391 前的形态）：

```go
entry["error"] = truncateErrMsg(redaction.RedactString(pr.Err.Error()), maxVerboseErrLen)
```

`pkg/redaction.RedactString` 对结构化 `key=value` 形式生效（password / token / DSN /
Authorization 等已注册 key），然后 `truncateErrMsg` 把字符串切到 512 rune
+ "..." 后缀。

### 三层结构性问题

| 层 | 问题 |
|----|-----|
| L1 代码 | `wrap.go:probePanicError` 返回 `fmt.Errorf("panic: %v", panicV)` 自身未脱敏；map[string]any 形态让任何 callsite 都可往 wire dependencies 里塞任意字段，funnel 不在边界关闭 |
| L2 PR  | 无 archtest 守 funnel；无 "secret 不出线" 反向用例测试 |
| L3 概念 | "4 通道"未在 ADR/规则形式化；errcode 3 层（Message/Details/Internal）+ ops-diagnostics slog 第 4 通道无独立可引用文档；`health.go:447` ref 注释"verbose breakdown is operator-only"与 line 426 行为相矛盾 |

### 框架对标反向证据

三家主流框架在 readyz/health verbose 响应中如何处理 dependency error 文本：

| 框架 | wire 上 error 文本 | 机制 | 文件 |
|------|------------------|------|------|
| Kubernetes apiserver healthz | ❌ 否 | wire 输出 `"[-]<name> failed: reason withheld\n"` (L274)；完整 error 仅入 klog (L275) | `staging/src/k8s.io/apiserver/pkg/server/healthz/healthz.go:253-315` |
| HashiCorp Vault `/sys/health` | ❌ 否 | `respondError(w, code, nil)` 第三参数 nil；只下发 structured status 字段（Initialized / Sealed / ClusterName）；敏感字段额外做 struct-level field gating | `http/sys_health.go:47-87, 267-284` |
| Grafana `/api/health` | ❌ 否 | `bool` 返回值丢全部 error 文本 | `pkg/api/health.go:9-23` |
| Prometheus promhttp（反例）| ✅ 是 | 直接 `err.Error()` 上 wire；其设计前提是 `/metrics` 仅内网暴露 | `prometheus/client_golang/prometheus/promhttp/http.go:538-544` |

GoCell 现行"在 wire 上发 redacted 文本"是相对激进的选择，且与自身 `health.go:447`
ref 注释（"failed checks do not surface in the 503 body; verbose breakdown is
operator-only"）相矛盾——代码自己破坏了自己的文档约定。

## §2 决策

### D1 — wire 不携带 error 文本，对齐 K8s 模式

`/readyz?verbose` 响应体的 `dependencies[*]` 字段集冻结为 `{status, duration_ms}`。
完整（脱敏后）error 文本只入 server-side slog "readyz unhealthy" 记录的
typed `dependencies` 字段。

理由：

1. **regex redaction 的盲区**：`pkg/redaction.sensitiveKeyPattern` 对结构化
   `key=value` 形式生效；对无 key 上下文的裸 token（JWT 子串、UUID 风格密钥、
   出现在自定义 key 名后的密文）必然漏。fail-closed 仅在 key 已知时成立。
2. **操作员 UX 不退化**：全文（redacted）error 经 server-side slog 落 SIEM /
   ELK / Datadog；操作员获取诊断的成本与之前一致。
3. **wire shape 稳定性**：减少 wire 字段 = 减少未来字段格式变更触发 stability surface。
4. **与自身 ref 注释 + 框架共识一致**。

### D2 — 四通道形式化（与 errcode 三层并列，新增 d 通道）

| 通道 | 载体 | 4xx 响应 | 5xx 响应 | 服务端 slog | 脱敏要求 |
|------|------|---------|---------|------------|---------|
| **a. Message** | `errcode.Message` (const literal) | 下发 | 下发 | 记录 | 不需要（const literal，无 runtime 数据） |
| **b. Details** | `errcode.Details` (`[]slog.Attr`) | 下发 | strip | 记录 | runtime 字段为程序员选择的低敏感字段（ID / 枚举 / 计数），无 raw secret |
| **c. Internal** | `errcode.WithInternal` | ❌ 不下发 | ❌ 不下发 | 记录 | runtime 调试信息（堆栈摘要、SQL 片段），仅服务端可见 |
| **d. Ops-Diagnostics** | handler-side `slog.Warn` 内的 typed payload | ❌ | ❌ | 记录 | **必须**经 typed funnel（health 包：`newRedactedErrorMsg(err) → redactedErrorMsg` 强制 `pkg/redaction.RedactString`） |

a/b/c 三层延续 ADR `202605051730-adr-errcode-message-pii-safety.md`；d 通道是
本 ADR 新增的形式化——handler 在 errcode envelope 之外独立向 slog 写出
"ops-diagnostics" 信息（典型场景：readyz verbose breakdown、recovery middleware
panic dump、outbox last_error sanitize）。该通道的安全模型由"typed funnel +
archtest 锁形态"双保险。

### D3 — Readyz 各字段的通道归属

| 字段 | 通道 | 载体 | 脱敏机制 |
|------|------|------|---------|
| HTTP status line（200 / 503 / 401） | a | net/http status code | — |
| `error.code` (`ERR_SERVICE_UNAVAILABLE`) | a | const literal | — |
| `error.message` ("service unavailable") | a | const literal | — |
| `error.details` (5xx 时 `[]`) | b | strip on 5xx (K#08) | — |
| `internal_reason` (e.g. "readyz status=unhealthy reason=readiness_failed") | c | `errcode.WithInternal` | — |
| slog `cells` map (`map[cellID]status`) | d | `slog.Any` | status 字符串本身非敏感 |
| slog `dependencies` map (`map[name]slogDependencyEntry`) | d | `slog.Any` | **ErrorMsg 字段类型 = `redactedErrorMsg`，由 `newRedactedErrorMsg` funnel 强制 `pkg/redaction.RedactString`** |
| slog `adapters` map (`map[role]info`) | d | `slog.Any` | adapter info 是部署期声明（in-memory / postgres / redis 等），无 runtime secret |
| wire body `dependencies[*]` (200 verbose) | a body fragment | `map[name]verboseDependencyEntry` | struct 字段集冻结无 error → wire 上**结构性**无 error 文本 |

### D4 — RETRACTS：旧 `health.go:426` 设计

撤回"在 wire 上发 redacted + truncated 文本"。原代码：

```go
entry["error"] = truncateErrMsg(redaction.RedactString(pr.Err.Error()), maxVerboseErrLen)
```

切换到：

```go
// wire view — 字段集冻结，无 error 字段
wire[name] = verboseDependencyEntry{Status: pr.Status, DurationMs: pr.Duration.Milliseconds()}
// slog channel d view — typed redacted funnel
slog[name] = slogDependencyEntry{
    Status:     pr.Status,
    DurationMs: pr.Duration.Milliseconds(),
    ErrorMsg:   newRedactedErrorMsg(pr.Err),
}
```

`truncateErrMsg` helper + `maxVerboseErrLen` 常量随 wire-error-text 一并删除——
slog 落盘容量不是问题，截断只在 wire 才有必要，wire 不发 error 文本就无需截断。

## §3 Threat Matrix

| Secret 形态 | 通道 a/b/c 暴露面 | 通道 d 暴露面 | 备注 |
|------------|-----------------|--------------|------|
| `password=hunter2`（结构化 key=value） | ✗ wire 不带文本 | ✓ funnel 经 RedactString，masked | 框架对齐 |
| `Authorization: Bearer eyJh...` | ✗ wire 不带文本 | ✓ funnel masked | authorizationPattern 覆盖到 EOL |
| 裸 JWT 子串（无 key 上下文，例 `expired token eyJhbGci...`）| ✗ wire 不带文本 | ⚠ funnel 不识别（regex 需要 key 锚），泄漏到 slog | 接受面：仅 server-side slog；与所有 regex-based redaction 同盲区；后续可加针对裸 base64url JWT 的 pattern |
| 裸 UUID API key（如 `failed to authenticate: 7a3c-...`）| ✗ wire 不带文本 | ⚠ 同上 | 接受面同上 |
| panic %v 含 secret | ✗ wire 不带文本（即便 runOneProbe wrap 成 `fmt.Errorf("panic: %v", panicV)` 也走 funnel） | ✓ funnel 适用，redacted | wrap.go:probePanicError 走 newRedactedErrorMsg 同路径 |
| `connection_string=...;Pwd=...;` 拼到 error message | ✗ wire 不带文本 | ✓ connectionStringPattern 整段消费到 \S+ 边界 | fail-closed 不在 ;/,断 |

⚠ 项是"已知盲区"（regex 类 redaction 共有），不在本 ADR 范围内解决；通过通道 a/b 完全不下发文本兜底——即便 d 通道 mask 漏，wire 仍不携带任何文本。

## §4 Enforcement Funnel Matrix

| InvariantID | 档 | 形态 | 上游 / 下游 | 文件 |
|-------------|----|------|-----------|------|
| `HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01` | **Hard** | typed struct field set frozen（FIELDS-FROZEN 范本，同 `OUTBOX-HANDLERESULT-FIELDS-FROZEN-01`） | 下游 Hard（archtest 锁字段集；加 error 字段需先改 allowlist + 改本 ADR）| `tools/archtest/health_verbose_shape_test.go` + `runtime/http/health/verbose_shape.go:verboseDependencyEntry` |
| `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01` | **Hard** | typed function call funnel（PANIC-REGISTERED 范本） | 上游 Hard（`redactedErrorMsg` unexported newtype，包外不可表达任何构造） + 下游 Hard（archtest 锁包内 conversion CallExpr 必在 `newRedactedErrorMsg` 函数体内 + 反向 fixture 锁字面量字段赋值）| `tools/archtest/health_verbose_shape_test.go` + `runtime/http/health/verbose_shape.go` |
| `HEALTH-VERBOSE-SCAN-COVERAGE-01` | Medium | fail-closed scan dir coverage（盲区清单反向自检，charter §3） | — | `tools/archtest/health_verbose_shape_test.go` |

## §5 ADR amendment 验证矩阵（与 ADR `202605051730` 关系）

本 ADR **扩展**而非 amend 现有 errcode-PII ADR。errcode 三层模型保持不变；新增的
ops-diagnostics 第 4 通道在 errcode 之外独立运作（handler 直接 `slog.Warn(...)`，
不经 errcode envelope）。因此现有 `202605051730` ADR 不需要 §"威胁矩阵" / §D
段重审；本 ADR §3 是 readyz 自身的新威胁矩阵，与之并列。

后续如有新 handler 引入"ops-diagnostics 通道"形态（典型场景：recovery middleware
panic dump、outbox last_error sanitize、auditquery payload redaction），该 handler
应：

1. 引入自己的 typed redacted 包装类型（同 `redactedErrorMsg`）
2. 注册自己的 archtest funnel（同 `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01`）
3. 在本 ADR §6 funnel matrix 表中追加条目

## §6 ref

- kubernetes `staging/src/k8s.io/apiserver/pkg/server/healthz/healthz.go:253-315`
  ([github](https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/server/healthz/healthz.go))
  — wire vs klog 双 buffer 隔离
- hashicorp/vault `http/sys_health.go:47-87, 267-284`
  ([github](https://github.com/hashicorp/vault/blob/main/http/sys_health.go))
  — `respondError(w, code, nil)` + struct-level field gating
- grafana/grafana `pkg/api/health.go:9-23`
  ([github](https://github.com/grafana/grafana/blob/main/pkg/api/health.go))
  — binary gate
- golang/go `src/net/url/url.go:1091-1103` — `URL.Redacted()` 单字段 sentinel 替换范本
- ADR `202605051730-adr-errcode-message-pii-safety.md` — errcode 三层 redaction
- ADR `202604242030-adr-kernel-wrapper-contract-observability.md` §8 — span redaction fail-closed
- `.claude/rules/gocell/observability.md` "errcode 三层 redaction" + "Readyz Verbose 四通道"
- `.claude/rules/gocell/ai-collab.md` Hard 范本（FIELDS-FROZEN / PANIC-REGISTERED）+ Funnel 双向锁评级
