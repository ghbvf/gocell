# ADR: Readyz Verbose 四通道分明 — wire 不带 error 文本 + slog 走 typed funnel

- Status: Accepted
- Date: 2026-05-17
- Tracks: PR391-HEALTH-VERBOSE-REDACTION-01（backlog item 在本 PR 同步移除，曾位于 backlog.md cap-04 HTTP 入站处理小节）
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
| **d. Ops-Diagnostics** | handler-side `slog.Log(ctx, level, ...)` 内的 typed payload（D8 起透传 request ctx 关联字段） | ❌ | ❌ | 记录 | **必须**经 typed funnel（health 包：`newRedactedErrorMsg(err) → redactedErrorMsg` 强制 `pkg/redaction.RedactString`） |

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
| slog `dependencies` map (`map[name]slogDependencyEntry`) | d | `slog.Any` | **ErrorMsg 字段类型 = `redactedErrorMsg`，由 `newRedactedErrorMsg` funnel 强制 `pkg/redaction.RedactString`**；适用于 degraded（Info 级）和 unhealthy（Warn 级）两条路径 |
| slog `adapters` map (`map[role]info`) | d | `slog.Any` | adapter info 是部署期声明（in-memory / postgres / redis 等），无 runtime secret |
| wire body `dependencies[*]` (200 verbose) | a body fragment | `map[name]verboseDependencyEntry` | struct 字段集冻结无 error → wire 上**结构性**无 error 文本；适用于 degraded 和 healthy 200 响应 |
| degraded 路径 slog level | d | `slog.LevelInfo` | `logDiagnostics(slog.LevelInfo, "readyz degraded")` — 操作员关注但不触发告警；unhealthy 路径用 `slog.LevelWarn` |

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

### D5 — `logUnhealthy` → `logDiagnostics` 重命名（PR #552 F3 修复）

`logUnhealthy` 只在 "unhealthy" 路径调用，导致 degraded probe 的 ErrorMsg 静默丢失：
`writeTo` 走 "degraded" → HTTP 200 分支但不触发 channel d slog，`slogDependencies` 的
ErrorMsg 从未被 operator 看到（F3 / PR #552 review P1 finding）。

修复：

1. `logUnhealthy` 重命名为 `logDiagnostics(level slog.Level, msg string, extra ...slog.Attr)`，
   level 参数区分 unhealthy（`slog.LevelWarn`）和 degraded（`slog.LevelInfo`）。
2. `writeTo` 在 "degraded" 路径也调用 `logDiagnostics`，确保 channel d 覆盖两种非健康状态。
3. unhealthy 路径额外传 `slog.String("reason", reason)` attr，保持既有 reason 字段。

### D6 — `slog.Group` 替换 `slog.Any(map)`（PR #552 round-5 架构修复）

round-1 / round-3 / round-4 一直用 `slog.Any("dependencies", map[string]SlogDependencyEntry{...})`
做 slog 序列化路径，看似简单但有结构性问题——slog 有两套 API：

| API | 序列化路径 | LogValue 生效 |
|-----|----------|--------------|
| `slog.Any(name, value)` | handler 拿 opaque blob 自决；JSON 走 `json.Marshal`（看不到 unexported 字段 → `{}`）；text 走 `fmt.Sprintf("%+v")`（reflect 能看 unexported 但是 CamelCase）| ❌ map 不是 LogValuer，永不调 |
| `slog.Group(name, attrs...)` | handler 内部 iterate Group attrs，每个 sub-attr 经 Resolve | ✓ 单个 LogValuer 在 sub-attr resolve 时被调 |

round-4 把 SlogDependencyEntry 字段 unexport 后，`slog.Any(map)` 路径产生致命 bug：
JSON handler 输出 `"dependencies":{"db":{}}`——所有字段丢失，operator 完全拿不到诊断信息
（PR #552 round-5 reviewer 指出）。

修复：`logDiagnostics` 改用 `slog.Group("dependencies", slog.Any(name, entry)...)`。
Group 内每个 sub-attr 的 value 是 SlogDependencyEntry（LogValuer），handler 在 Resolve 阶段
调 LogValue 返回 GroupValue，所有 handler（JSON / text / logfmt）一致输出 snake_case。
text 输出形态（probe error `dial failed password=hunter2 host=mq` 脱敏后）：
`dependencies.db.status=unhealthy dependencies.db.duration_ms=0 dependencies.db.error_msg="dial failed password=<REDACTED> host=mq"`
（text handler 对含空格 / `=` 的值加引号；空 error_msg 输出 `error_msg=""`，单 token 无特殊字符如 `error_msg=timeout` 则不加引号）。
JSON 输出形态：`"dependencies":{"db":{"status":"unhealthy","duration_ms":0,"error_msg":"dial failed password=<REDACTED> host=mq"}}`。

healthtest 包内 `ReadyzUnhealthyDeps` + `HasReadyzDependencyStatus` + health 包本地
`readyzUnhealthyDeps` helper 全部改用 `slog.Value.Group()` 遍历——而非旧的 type-assert
`map[string]SlogDependencyEntry`——以匹配新 Group shape。

### D7 — 删除 `NewSlogDependencyEntryForTesting`（PR #552 round-5 架构修复）

round-4 引入 `NewSlogDependencyEntryForTesting` 作为 healthtest unit test 的 opt-out
构造函数，配合 archtest allowlist 允许其 callsite 调 `redactedErrorMsg(...)`。但 reviewer
正确指出：函数 exported 在 production 文件——任何 production 包都能调用，"ForTesting"
后缀只是 convention 非 enforcement。Hard 上游 "compile-time 不可表达" 被这个 backdoor 破坏。

修复：彻底删 `NewSlogDependencyEntryForTesting`。healthtest unit test 改用
`SlogDependencyEntry{}` zero value 做 plumbing 测试（验证 helper 正确遍历 Group
结构 + 返回正确 key/value 类型），语义内容测试（Status/ErrorMsg 真实值）放到 health
package 自己的白盒测试里，通过 real Handler 构造（`TestSlogDependencyEntry_AccessorsViaRealHandler`）。

upstream Hard 现在没有任何 backdoor：SlogDependencyEntry 三个字段 unexported + 无
exported 构造函数 + redactedErrorMsg 是包私有 newtype——外部包构造该类型在 Go 编译器
层完全不可表达。

### D8 — `logDiagnostics` 透传 request ctx（#942 R2 修复）

D5 把 `logUnhealthy` 改名 `logDiagnostics` 时，slog 调用仍是
`slog.Log(context.Background(), level, msg, ...)`。wire 删 error 文本后（D1），slog 是
**主诊断通道**——用 `context.Background()` 会丢框架 contextHandler
（`runtime/observability/logging`）从 ctx 注入的 `request_id` / `trace_id` /
`correlation_id`，使 503 / degraded 记录无法关联到触发它的请求（PR #552 review R2）。

修复：`logDiagnostics` 加 `ctx context.Context` 首参，`writeTo`（持有 request ctx）的
degraded / unhealthy 两条调用路径都传入，末行改 `slog.Log(ctx, level, msg, ...)`。关联
字段来源与 errcode `WithInternal` 路径**同源**（同一 contextHandler）。同 readyz 路径的
shutting-down / singleflight-error / panic-recover 三处包级 slog 调用一并改用
`slog.InfoContext` / `slog.ErrorContext`，消除 readyz 路径全部 ctx 丢失点。

ctx 仅承载关联值（非 secret），不引入新泄漏面——见 §4 威胁矩阵 #942 重评。

## §3 Threat Matrix

| Secret 形态 | 通道 a/b/c 暴露面 | 通道 d 暴露面 | 备注 |
|------------|-----------------|--------------|------|
| `password=hunter2`（结构化 key=value） | ✗ wire 不带文本 | ✓ funnel 经 RedactString，masked | 框架对齐 |
| `Authorization: Bearer eyJh...` | ✗ wire 不带文本 | ✓ funnel masked | authorizationPattern 覆盖到 EOL |
| `Authorization: Basic <b64>`（HTTP Basic） | ✗ wire 不带文本 | ✓ funnel masked | 同 `authorizationPattern` 覆盖到 EOL，key 匹配大小写不敏感 |
| 裸 JWT 子串（无 key 上下文，例 `expired token eyJhbGci...`）| ✗ wire 不带文本 | ⚠ funnel 不识别（regex 需要 key 锚），泄漏到 slog | 接受面：仅 server-side slog；与所有 regex-based redaction 同盲区；后续可加针对裸 base64url JWT 的 pattern |
| mTLS / TLS 证书 PEM 块（无 key 上下文，如 `-----BEGIN CERTIFICATE-----\n...`） | ✗ wire 不带文本 | ⚠ funnel 不识别（regex 需要 key 锚），泄漏到 slog | 接受面同上；PEM 块本身是证书（公钥），私钥（`-----BEGIN RSA PRIVATE KEY-----`）若无 key 锚同样盲区；纵深：probe 应避免在 error 中输出完整 PEM |
| 裸 UUID API key（如 `failed to authenticate: 7a3c-...`）| ✗ wire 不带文本 | ⚠ 同上 | 接受面同上 |
| panic %v 含 secret | ✗ wire 不带文本（即便 runOneProbe wrap 成 `fmt.Errorf("panic: %v", panicV)` 也走 funnel） | ✓ funnel 适用，redacted | wrap.go:probePanicError 走 newRedactedErrorMsg 同路径 |
| `connection_string=...;Pwd=...;` 拼到 error message | ✗ wire 不带文本 | ✓ connectionStringPattern 整段消费到 \S+ 边界 | fail-closed 不在 ;/,断 |

⚠ 项是"已知盲区"（regex 类 redaction 共有），不在本 ADR 范围内解决；通过通道 a/b 完全不下发文本兜底——即便 d 通道 mask 漏，wire 仍不携带任何文本。

### SIEM/ELK 转发：redactor 覆盖面 vs 真实泄漏面

通道 d（slog）是 wire 删 error 文本（D1）之后的**主诊断通道**，因此必须诚实区分 redactor
*覆盖到*的形态与 *覆盖不到*的真实泄漏面——二者不能混为一谈：

- **覆盖面（已脱敏）**：结构化 `key=value` 形态的 secret——`password=` / `Authorization:` /
  `connection_string=` / JSON quoted key（见 `pkg/redaction.sensitiveKeyPattern`）。这类
  value 段被替换为 `<REDACTED>`，原始 secret 文本不出现在日志流（§3 矩阵前三行 + panic /
  connection_string 行）。
- **真实泄漏面（已知盲区，会进 slog）**：无 key 锚的裸 token——裸 JWT 子串、PEM 块、
  裸 UUID API key（§3 威胁矩阵 ⚠ 三行）。regex redaction 必须有 key 锚才能命中，这类
  token **会原样写入 slog**。这不是「无泄漏路径」，而是「泄漏面收敛到 server-side slog
  单一通道，且 wire 结构性永不携带任何 error 文本兜底」。

接受该盲区的依据是**纵深防御，非「已消除」**：

1. **wire 兜底**：即便 d 通道 mask 漏，通道 a/b 在 wire 上结构性不下发任何 error 文本
   （`verboseDependencyEntry` 字段集冻结无 error 字段）——公网客户端永远拿不到。
2. **通道受限**：slog 落 SIEM / ELK / Datadog，是运维受限的内部诊断面，不对外暴露。
3. **probe 作者纵深**：probe error message 应避免硬编码完整 secret / PEM / 裸 token
   （§3 备注列的纵深指引）；后续可加针对裸 base64url JWT 的 pattern 进一步收窄盲区。

`<REDACTED>` fingerprint：mask 后为固定字面量，长度恒定，不反推原始 value 长度，不构成
旁信道；`error_msg` 含 `<REDACTED>` 是 secret 已 mask 的正常形态，非告警信号。

> **纠正（#942 R3）**：本节早期版本曾断言「不存在通过日志泄漏 secret 的路径」，与 §3
> 威胁矩阵 ⚠ 三行（裸 JWT / PEM / UUID 泄漏到 slog）直接矛盾——运维若以前者为准会误判
> d 通道已 fail-closed。该绝对化断言已删除；secret 泄漏面的真值以 §3 威胁矩阵为准。

## §4 Enforcement Funnel Matrix

| InvariantID | 档 | 形态 | 上游 / 下游 | 文件 |
|-------------|----|------|-----------|------|
| `HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01` | **Hard** | golden 字面量锁 ×2：typed struct 字段集冻结（FIELDS-FROZEN 范本，同 `OUTBOX-HANDLERESULT-FIELDS-FROZEN-01`）+ **每字段 json tag 首段字面量冻结**（#947 补——wire 字段名由 json tag 决定，非 Go 字段名） | **下游 Hard**（archtest 锁字段集 **和** json tag 字面量；加/改 wire 字段需先改 `healthVerboseWireAllowedFields` + `healthVerboseWireJSONTags` + 改本 ADR）。**上游**：FIELDS-FROZEN golden 锁非 caller-funnel，无独立 Hard 上游义务——`verboseDependencyEntry` 是 pkg-private struct（type-system），且 readyz handler 始终 `json.Marshal` 该 struct 产 wire body（单 handler 约定，非 archtest 强制；改用别的 wire shape 由 code review 兜底）。| `tools/archtest/health_verbose_invariants_test.go`（`TestHealthVerboseWireFieldSetFrozen` + `TestHealthVerboseWireJSONTagsFrozen`）+ `runtime/http/health/verbose_shape.go:verboseDependencyEntry` |
| `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01` | **Hard** | typed 创建点 funnel（conversion + untyped-const inflow）+ body-redaction form-lock + 字段类型 linchpin + funnel-sig 反 vacuous（#947→#996：纯 AST ident 升级为 go/types 四锁）+ unexported struct fields | **上游 Hard**：`SlogDependencyEntry` 三个字段全部 unexported（status / durationMs / errorMsg）+ `redactedErrorMsg` 是包私有 newtype，外部包无法命名类型或寻址字段，**Go 编译器是 gate**（reflect 是唯一理论旁路，包内用 reflect 是 self-inflicted bug，code review 兜底，盲区 b/h/i）。**下游 Hard（#996 四锁 go/types 守卫）**：① **创建点收口**——**迭代 `info.Types`**（类型检查器完整 expr→type 记录，无 per-AST-node-kind 盲区）找出任一 `redactedErrorMsg` 创建：`redactedErrorMsg(x)` 转换（`info.Types[fun].IsType()`+named 同一性，`types.Unalias` alias 透明、免疫 shadow）**或** 类型为 redactedErrorMsg 的常量（`Value!=nil`——untyped 字符串经上下文获得该类型的**任意 AST 形态**：literal / **const-ident** `const c="x";var _ redactedErrorMsg=c` / const concat / paren，无 conversion CallExpr 的入流）必在 `newRedactedErrorMsg` **body 源码区间内**（位置判定，非 func-name skip）。#996 复审实测 const-ident 旁路绕过早期 BasicLit/CallExpr 节点枚举走查，改 info.Types 迭代后闭合；② `errorMsg` 字段类型必为 `redactedErrorMsg`（linchpin——退 `string` 即 funnel vacuous）；③ `newRedactedErrorMsg` 必存在且签名 `func(error) redactedErrorMsg`（反 vacuous）；④ **body 必脱敏**——funnel 体内每个 `redactedErrorMsg(x)` 的 `x` 必是 `redaction.RedactString(...)` 调用（form-uniqueness；①②③ 不验证 body 真脱敏，去掉 RedactString 返回裸 `err.Error()` 会让 ①②③ 全绿）。| `tools/archtest/health_verbose_invariants_test.go`（`TestHealthRedactedErrorMsgCreationFunnel` / `...FunnelBodyRedacts` / `...FieldTyped` / `...FunnelFuncSig`）+ `runtime/http/health/verbose_shape.go` |

**上游 Hard 论据更新历史**：

- **PR #552 round-1**：ErrorMsg 导出字段 + redactedErrorMsg unexported 类型组合，仅做到「下游 Hard + 上游 Medium」——外部包可通过 untyped const 隐式转换 `health.SlogDependencyEntry{ErrorMsg: "raw"}` 绕过（reviewer round-4 指出）。
- **round-4**：把 SlogDependencyEntry 三个字段全部 unexported，外部只通过 read-only accessor methods 消费——但同时为 healthtest unit test 加了 `NewSlogDependencyEntryForTesting` exported 构造 + archtest allowlist。reviewer 立即指出这是 production 文件的 exported function，任何 production 包都能调用——"ForTesting" 后缀只是 convention 非 enforcement，Hard 上游 backdoor。
- **round-5（本 PR 终态）**：彻底删 `NewSlogDependencyEntryForTesting`；healthtest unit test 改用 zero-value plumbing 测试，语义测试改在 health 包内 white-box 用 real Handler 构造。同步删 archtest allowlist 条目。上游真 Hard：unexported 字段 + 无任何 exported 构造函数 + redactedErrorMsg 包私有 newtype + 无 testing backdoor。Go 编译器是唯一 gate。
- 同步删两个 reverse archtest（TestHealthRedactedErrorMsgFunnelLiteralReverse + TestHealthRedactedErrorMsgFunnelPackageLevelVarReverse，round-3 加的）因 compile-time 已不可表达。盲区清单 (c) 整合到 forward 规则（FuncDecl.Body + GenDecl 双扫）。
- **#947（archtest 硬化，下游 Hard 升级）**：round-5 的下游 forward 规则是**纯 AST**——只匹配 `*ast.Ident{Name:"redactedErrorMsg"}`，且文件头 godoc 自述「pure AST, no go/types — unexported closes the boundary, 足够」。PR #552 R1 复审证伪该论断：unexported 上游边界**不**阻止三类**包内**回归——字段类型退回 `string`（funnel 被旁路但 AST 规则仍绿）、`newRedactedErrorMsg` 删除/改名（零调用点 → vacuous green）、同名局部符号 shadow。#947 把下游升级为 **go/types 守卫**（初版三重 conversion/字段类型/funnel-sig；后经 #996 复审补为**四锁**，见下条），并重写文件头矛盾 rationale（不留两套真值源）。上游论据不变（仍是 Go 编译器 gate）。**`HEALTH-VERBOSE-SCAN-COVERAGE-01` 同步删除**：其唯一作用（surface 类型 relocation 致 gate vacuous）已被各规则的内生反 vacuous 守卫吸收——wire-shape 在 `scanVerboseShape` 找不到 struct 时 `t.Fatalf`，funnel 在 `Scope().Lookup` 返回 nil（类型/字段/funnel 函数缺失）时 `t.Fatalf`；独立 scope sanity gate 已是死代码。
- **#947（WIRE-SHAPE json-tag 锁）**：round-5 的 `HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01` 只锁 Go 字段名 `{Status, DurationMs}`，但 wire 字段名实际由 json tag 决定——改 `json:"status"`→`json:"state"` 而保留 Go 字段名会让 wire 漂而 archtest 绿。#947 增 `TestHealthVerboseWireJSONTagsFrozen` 锁每字段 json tag 首段字面量（`healthVerboseWireJSONTags`），并把 wire-shape 拆为 field-set / json-tag 两个独立 Test（每属性独立失败归因，且各函数 gocognit ≤15）。
- **#996（复审证伪三重 Hard，补为四锁）**：同一 PR 内合并前复审（C1/C2/C3）实测推翻 #947 初版下游"三重 Hard"声明——三重**不完整**且 Hard 被高估：(F1) 只锁显式 `redactedErrorMsg(x)` conversion CallExpr，漏**包内 untyped-const 入流**（`SlogDependencyEntry{errorMsg:"raw"}` / `var x redactedErrorMsg="raw"` / `return "raw"`，均合法编译且旧规则绿）——初版 blind-spot (a)「无 conversion 不可赋值」是**事实错误**；(F2) 签名锁不验 body，去掉 `RedactString` 返回裸 `err.Error()` 时三重全绿，**核心脱敏不变量完全无守卫**。#996 修复：guard ① 升为"创建点 funnel"、新增 guard ④ body-redaction form-lock，重写 blind-spot (a)。**二轮复审**实测发现初版 guard ① 的 BasicLit/CallExpr **节点枚举**走查漏 const-ident 间接（`const c="raw";var _ redactedErrorMsg=c`，正常 Go 惯用法）——遂改为**迭代 `info.Types`**（类型检查器完整记录，无 per-node-kind 盲区）+ 位置判定 funnel 区间，闭合 const-ident/concat 等全部常量入流形态；残留盲区收敛为仅 (b) reflect / (h) 泛型（均 bypass-only 构造，code-review 兜底）。mutation-RED 由 5 扩到 9（+untyped-const-inflow +const-ident +drop-RedactString +map-parity）全红验证。另：F3 补 `healthVerboseWireAllowedFields`↔`healthVerboseWireJSONTags` key parity（`TestHealthVerboseWireMapsParity`，防 allowlist 加字段漏配 tag-map 致 tag 不锁）；F4 wire-shape 违规回 `Diagnostic{Rel,Line}`/`Report`（#996 重构成裸 string helper 时丢了 production 行号，回退）。

**威胁矩阵（§3）重评**：#947→#996 是 enforcement **强化**，无 ✅→⚠️ 回归。§3 所有行依赖两条前提：(P1)「通道 d 的每个 error 文本值都由 `newRedactedErrorMsg` 创建」和 (P2)「`newRedactedErrorMsg` 真的调用 `RedactString` 脱敏」。**纠正先前版本的过度声明**：曾写「guard ②（字段类型）把该前提机器强制」——这是错的。guard ②（`errorMsg` 类型必为 `redactedErrorMsg`）只保证字段**类型**，既不保证创建点收口（P1）也不保证 body 脱敏（P2）。真正的机器强制是：**guard ①（创建点 funnel）强制 P1**（含 untyped-const 入流，非仅显式 conversion——#996 F1 补；先前"字段类型即足够"漏了包内 `SlogDependencyEntry{errorMsg:"raw"}` / `var x redactedErrorMsg="raw"` 入流），**guard ④（body-redaction form-lock）强制 P2**（#996 F2 补；先前无任何 guard 验证 body 调 RedactString，去掉它 ①②③ 全绿）。guard ②/③ 是 ① 的反 vacuous 支撑（字段退 string / funnel 消失则 ① 失守）。四锁齐备后 §3 各行所依赖的 funnel **应用性 + 脱敏性**才真正从「文档断言」升级为「archtest 强制」。

**威胁矩阵（§3）重评 — #942 R2/R3 amendment**：本次 amendment 是「文档纠正（R3，重写
§SIEM/ELK）+ slog ctx 透传（R2，D8）」，**逐行重评无 ✅→⚠️ 回归**：

- §3 八行的 a/b/c 列「✗ wire 不带文本」全部不变（wire shape 未动，`verboseDependencyEntry`
  字段集仍冻结）。
- d 列三个 ⚠ 盲区行（裸 JWT / PEM / UUID）表述本就准确；本次仅把 §SIEM/ELK 与之**对齐**
  ——删除矛盾的「无泄漏路径」绝对化断言，§SIEM/ELK 是被纠正方，§3 矩阵是真值源。无格子
  从 ✅ 变 ⚠️。
- D8 的 ctx 透传只新增 `request_id` / `trace_id` / `correlation_id` 关联字段到 slog
  record。这些是关联 ID（非 secret，本就由 contextHandler 在全框架所有 slog record 注入），
  不携带 probe error 文本，**不扩大 d 列暴露面**。
- (P1)(P2) 两条前提仍由 guard ①④ 机器强制，未受本次 amendment 影响。

**slog 序列化路径**：见 §2 D6 — `slog.Group("dependencies", slog.Any(name, entry)...)` 是唯一让 LogValue 真生效的 slog idiom，所有 handler 输出一致 snake_case。**严禁退回 `slog.Any("dependencies", map)` 形态**——unexported 字段 + JSON handler 会输出 `{}`，所有诊断信息丢失（round-4 实测 bug）。

## §5 ADR amendment 验证矩阵（与 ADR `202605051730` 关系）

本 ADR **扩展**而非 amend 现有 errcode-PII ADR。errcode 三层模型保持不变；新增的
ops-diagnostics 第 4 通道在 errcode 之外独立运作——handler 经
`slog.Log(ctx, level, ...)`（D8 起透传 request ctx）写出 typed payload，不经 errcode
envelope。因此现有 `202605051730` ADR 不需要 §"威胁矩阵" / §D 段重审；本 ADR §3 是
readyz 自身的新威胁矩阵，与之并列。

后续如有新 handler 引入"ops-diagnostics 通道"形态（典型场景：recovery middleware
panic dump、outbox last_error sanitize、auditquery payload redaction），该 handler
应：

1. 引入自己的 typed redacted 包装类型（同 `redactedErrorMsg`）
2. 注册自己的 archtest funnel（同 `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01`）
3. 在本 ADR §4 funnel matrix 表中追加条目

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
- `.claude/rules/gocell/ai-robust.md` Hard 范本（FIELDS-FROZEN / PANIC-REGISTERED）+ Funnel 双向锁评级
