# Backlog 增补（2026-04-26 分层六席位审查）

> 来源: `bak/20260426-layered-six-role-review/`（kernel/cells/runtime/adapters/pkg/cmd/contracts 7 层全仓审查 + 6 角色席位并行）
>
> 基线: `develop @ be5c4c2`（layered-full-scan，1133 changedFiles）
>
> 状态: **草案（已按最近 PR 回写进度）** — 待用户确认后回灌到 `docs/backlog.md` + `docs/plans/202604252100-026-post-v1.0-cleanup-plan.md` + `docs/plans/202604260058-l4-virtual-taco.md`
>
> P1 总数: **31 条**（已登记 5 + 新发现 26）
>
> 进度更新（2026-04-27）: 按最近合并 PR `#293/#294/#295/#296/#297/#298/#300/#301/#302` 回写；§2 新发现 26 条中 **已完成 11 条**，§3 既有 PR 扩充 3 条中 **已完成 1 条**。

---

## §1 已登记 / 已计划（5 条，本草案不重写）

| 报告 P1 | 现有归属 | 状态 |
|---|---|---|
| runtime P1.1 internal/health 默认 fail-open | **PR-A46** INTERNAL-GUARD-REQUIRED | 026 plan Wave 6，⬜ 待启动 |
| runtime P1.2 verbose 语义不一致 | **PR-A53**（吸收 PR-CFG-A-DEFER-1） | 026 plan Wave 8，⬜ 待启动 |
| contracts P1.1 internal 调用方语义不清 | **PR-CFG-I.X2**（含 G2-FU1） | L4 plan，⬜ 等 G1+G2 |
| cells P1.1 setup 公网入口 | **A26-R2** SETUP-ADMIN-RATE-LIMIT + 路径已迁 #284 | backlog 触发条件项，🟠 |
| kernel P1.4 sweeper onError + 并发 | **PR252-F2** COMMAND-SWEEPER-PRODUCTION-GOVERNANCE | backlog Wave 10，🟠 触发 |

---

## §2 新发现 P1（26 条，按 7 主题打包；已完成 11 条）

### §2.1 PR-SEC-1 FAIL-CLOSED-DEFAULTS（Cx3，~12h，🔴 发布前必做，3/4 已完成）

**抽象**：跨层"安全默认值偏 fail-open"统一收口；real mode 下凭据传输强制加密、未配置即启动失败。

**清零**：adapters P1.1 / adapters P1.3 / cmd P1.4 / kernel P1.1

| 子项 | 状态 | 证据 | 修复方向 |
|---|---|---|---|
| **SEC-TLS-REAL-MODE-01** | ✅ 已完成（PR #297/#301） | `adapters/redis/client.go:175` + `adapters/vault/transit_provider.go:756` + `adapters/s3/s3.go:55` | real mode 强制 TLS/HTTPS；非加密 endpoint startup fail-fast；写新 errcode `ERR_ADAPTER_INSECURE_TRANSPORT` |
| **SEC-WEBSOCKET-DEFAULT-01** | ✅ 已完成（PR #297/#301） | `adapters/websocket/handler.go:37,57` | origins 默认拒绝 + 显式 dev opt-in；注册失败分支主动 `conn.Close()` |
| **SEC-CONTROLPLANE-ADDRBOUND-01** | ✅ 已完成（PR #297） | `cmd/corebundle/controlplane.go:58` + `cmd/corebundle/bundle.go:117` | 安全强约束改地址驱动（非回环 → 必须 auth chain），不再绑 adapter mode；mode dev + 0.0.0.0 监听亦必须 fail-fast |
| **SEC-WRAPPER-REDACTOR-DEFAULT-01** | ✅ 已完成（PR #366 refactor/512） | `kernel/wrapper/consumer.go` + `kernel/wrapper/lifecycle.go` + `pkg/redaction/` | 默认改 fail-closed `pkg/redaction.RedactError` 硬编；删整条 opt-out wiring（`ErrorRedactor` type / `WithConsumerErrorRedactor` / `bootstrap.WithErrorRedactor` / `middleware.WithErrorRedactor` / `Bootstrap.errorRedactor` / `ContractTracingMiddleware` 第二参）— 比原方案更激进（比 `WithRedactorDisabled(devOnly)` 走得更远），对齐 Vault `log_raw=false` + Go stdlib `URL.Redacted()` 哲学 |

**对标**：Vault server `tls_disable` 显式拒绝 production；Envoy `route.config.virtual_hosts[].typed_per_filter_config` 默认拒绝；K8s `--insecure-port=0` 默认行为；Vault `audit log_raw=false` 默认硬编 + Go stdlib `URL.Redacted()` 无 opt-out（PR #366 实施版）

### Follow-up（PR #366 reviewer registered）

- **SPAN-RECORD-ERROR-REDACT-ARCHTEST-01** ✅ 已完成（PR #366 同 PR 增量）— `tools/archtest/span_record_error_redact_test.go` 纯 AST scan：扫 `kernel/wrapper/` + `runtime/http/middleware/` 非测试 .go 文件，断言每个 `span.RecordError(...)` 调用首参必须 inline `redaction.RedactError(...)`；附 fixture compliant/violates 自验证。在 PR fix 增量过程中即抓出 `tracing.go:306` 一处中间变量持有 redact 结果的旁路，已 inline 修复。

---

### §2.2 PR-CONTRACT-INTEGRITY（Cx3，~12h，🔴 发布前必做，3/6 已完成）

**抽象**：契约执行闭环 — fail-fast 取代静默 no-op，活跃端点测试映射门禁，运行时与元数据一致性。

**清零**：pkg P1.2 / contracts P1.4 / cells P1.2 / cells P1.4 / contracts P1.3

| 子项 | 状态 | 证据 | 修复方向 |
|---|---|---|---|
| **CONTRACTTEST-SCHEMAREF-FAILFAST-01** | ⬜ 待处理 | `pkg/contracttest/contracttest.go:170,189` | schemaRefs key 未命中默认 fail（拒绝 no-op）；宽松行为改显式 API `WithMissingKeyTolerated()` |
| **CONTRACT-ENDPOINT-TEST-MAPPING-01** | ⬜ 待处理 | `contracts/http/config/update/v1/contract.yaml:1` | 治理新规则：所有 `lifecycle: active` 的 HTTP contract 必须有对应 contract test 用例（活跃端点 → 测试用例映射门禁）|
| **CONTRACT-PATH-QUERY-EXECUTABLE-01** | ⬜ 待处理 | `pkg/contracttest/contracttest.go:206` | path/query 参数约束（pattern / min / max / format）必须有 transport 入参可执行测试（rejected 用例覆盖）|
| **AUDITAPPEND-SLICE-METADATA-DRIFT-01** | ✅ 已完成（PR #294） | `cells/auditcore/slices/auditappend/service.go:32` + `slice.yaml:20` | auditappend 实际订阅多 topic，但 slice.yaml `contractUsages` 仅声明 1 个 → 同步补齐声明（治理元数据单一事实源）|
| **AUDITQUERY-USERPATCH-CONTRACT-DRIFT-01** | ✅ 已完成（PR #300） | `cells/auditcore/slices/auditquery/handler.go:88` + `contracts/http/audit/list/v1/contract.yaml:13` + `cells/accesscore/slices/identitymanage/handler.go:220` | (a) auditquery handler 接受 contract 未声明的 queryParams → 同步扩展 contract；(b) identitymanage user patch 用宽松 `map[string]any` 解码 → 改显式 DTO `DisallowUnknownFields` |
| **CONTRACT-INPUT-CONSTRAINT-01** | ✅ 已完成（PR #298/#301） | `contracts/http/auth/login/v1/request.schema.json:6` + `contracts/http/config/list/v1/contract.yaml:17` | 凭据字段补 `minLength` / `maxLength` / `pattern`；分页 `limit` 全局上限 500（写治理规则）|

**对标**：JSON Schema spec keyword；Pact contract test "must have" 模式；K8s admission webhook strict mode

---

### §2.3 PR-V1-EVOLVE-ADR（Cx2，~3h，🟡 v1.0 GA 前，1/1 已完成)

**抽象**：v1 响应 schema 普遍 `additionalProperties:false`（PR-CFG-E #278 加的）与 `.claude/rules/gocell/api-versioning.md:12` "v1 只增不删 / 新增可选字段不破坏 v1" 硬冲突——需 ADR 决策走向。

**清零**：contracts P1.2

**状态**：✅ 已完成（PR #353 G5 / 029 PR-CI-3-V1-RESPONSE-EVOLVE）— 方向 A 落地

**实际落地**：
- ADR `docs/architecture/202605031600-adr-v1-schema-evolution.md` 落地方向 A
- `kernel/governance/rules_strict.go::FMT-20` 收窄到 request-only（lenient response/event, strict request）
- 30 response/event schema 顶层放宽 `additionalProperties: true`，request schema 保持 `false`
- 共享 error envelope（`contracts/shared/errors/error-response-v1.schema.json`）例外保持 strict
- `B2-C-08` cell event decoder `DisallowUnknownFields` 同步关闭
- `.claude/rules/gocell/api-versioning.md` 同步规则文档

**对标**：Stripe API "we never make breaking changes to v1" + 服务端始终允许新字段；GitHub REST v3 同模式；OpenAPI 3.x `additionalProperties` 推荐 unset 表示宽松（与最终方向 A 一致）

---

### §2.4 PR-LIFECYCLE-ROBUSTNESS（Cx3，~12h，🟡 v1.0 GA 前，3/5 已完成)

**抽象**：startup/shutdown 失败语义、声明阶段 panic 隔离、worker 静默退出、adapter 连接预算 — 5 条生命周期健壮性问题统一处理。

**清零**：runtime P1.3 / runtime P1.4 / runtime P1.5 / adapters P1.2 / adapters P1.4

| 子项 | 状态 | 证据 | 修复方向 |
|---|---|---|---|
| **STARTUP-ROLLBACK-ERR-JOIN-01** | ⬜ 待处理 | `runtime/bootstrap/run_state.go:113,121` | startup 失败时 `errors.Join(startupErr, rollbackErr)` 并入返回；或定义 `StartupRollbackError{Startup, Rollback error}` 结构化错误 |
| **EVENTROUTER-PANIC-ISOLATE-01** | ✅ 已完成（PR #296） | `runtime/eventrouter/router.go:115` + `runtime/bootstrap/bootstrap_phases.go:1002` | `RegisterSubscriptions` 协议改 error；或在 phase 调用层 recover 并转 cell 装配错误（与已有 cell init 失败语义一致）|
| **WORKER-NIL-EXIT-EXPLICIT-01** | ✅ 已完成（PR #296） | `runtime/worker/worker.go:59` + `runtime/bootstrap/bootstrap_phases.go:1407` | worker 提前 nil 退出建模为 `ErrWorkerExitedEarly`，附 worker 名 + 退出阶段；区分"主动 stop"与"意外 nil 退出"|
| **ADAPTER-CONNECT-BUDGET-01** | ⬜ 待处理 | `adapters/rabbitmq/connection.go:273` + `adapters/postgres/pool.go:69` | adapter 级强制 `ConnectTimeout`（默认 5s），不依赖上层 ctx；写入 Config 接口 + Validate；超时返回 `ERR_ADAPTER_CONNECT_TIMEOUT` |
| **POSTGRES-REFRESHSTORE-NOPANIC-01** | ✅ 已完成（PR #296） | `adapters/postgres/refresh_store.go:116` | 构造器 panic → error-first；提供 `Must*` 包装给组合根可选使用 |

**对标**：fx `Lifecycle.Append(Hook{OnStart, OnStop})` 错误聚合；K8s `apiserver/pkg/server.PostStartHook` 错误返回；database/sql `OpenDB` error-first

---

### §2.5 PR-TEST-DEPTH（Cx3，~10h，🟡 v1.0 GA 前，0/3 已完成)

**抽象**：补齐 L2 原子失败 / 故障注入 / AEAD 负向 三组测试缺口。

**清零**：cells P1.6 / adapters P1.5 / pkg P1.3

| 子项 | 状态 | 证据 | 修复方向 |
|---|---|---|---|
| **AUDITAPPEND-L2-FAILURE-PROOF-01** | ⬜ 待处理 | `cells/auditcore/slices/auditappend/outbox_test.go:50` | PG-level 失败注入：DB 写成功 + outbox 失败 → 事务真回滚（用 testcontainer + 故意 fail outbox writer）|
| **S3-FAILURE-INJECTION-01** | ⬜ 待处理 | `adapters/s3/s3.go:114` + `adapters/s3/s3_test.go:11` | MinIO testcontainer 集成测试：上传 403/5xx/timeout/recovery 路径覆盖 |
| **SECURECOOKIE-AEAD-NEG-01** | ⬜ 待处理 | `pkg/securecookie/securecookie.go:128,134` + `pkg/securecookie/securecookie_test.go:35` | AEAD 负向用例：截断 / 伪造 / 边界长度 / 解密失败类型断言（`errors.Is(err, ErrAEADAuthFailed)` 等）|

---

### §2.6 PR-API-CONSISTENCY（Cx3，~8h，🟡 v1.0 GA 前，2/3 已完成)

**抽象**：跨层 API 失败语义统一 + 错误分类单一真相源 + Journey 验收 strict 强制。

**清零**：kernel P1.3 / pkg P1.1 / kernel P1.2

| 子项 | 状态 | 证据 | 修复方向 |
|---|---|---|---|
| **WRAPPER-CELL-API-ERROR-FIRST-01** | ✅ 已完成（PR #296） | `kernel/wrapper/handler.go:56` + `kernel/wrapper/spec.go:46` + `kernel/cell/auth_plan.go:107` | 配置期 fail-fast 统一 error-first；提供 `MustNewSpec` / `MustNewHandler` 给组合根使用；godoc 写明 panic 仅在 `Must*` 路径 |
| **ERRCODE-CLASSIFY-SINGLE-SOURCE-01** | ✅ 已完成（PR #368） | `pkg/errcode/classify.go` + `pkg/errcode/errcode.go` | PR#368 (refactor/514-pkg-breaking-cleanup) 删除 `expected4xxCodes` whitelist；`IsExpected4xx` 改 `ec.Kind.IsClient()` 单源；`Error.Status()` / `Error.PublicCode()` 同 Kind 派生；4xx 分类与 HTTP status 映射收敛到 Kind 一处 |
| **JOURNEY-VERIFY-STRICT-AUTO-01** | ✅ 已完成（PR #295） | `kernel/verify/runner.go:160` + `kernel/governance/rules_verify.go:310` | strict 模式（CI）要求每个 journey 至少 1 条 `auto` check；manual pending 现仅 warning → strict 下 fail-fast |

**注**：与 backlog `PR220-3 JOURNEY-VERIFY-FAIL-CLOSED-01` 协同 — 220-3 是 RunPattern + skip stub 检测；本节是 manual-only 不允许混入 CI strict。

---

### §2.7 PR-CLI-CONSISTENCY（Cx2，~5h，🟡 可延后，0/3 已完成)

**抽象**：CLI 帮助一致性 + 未实现命令隐藏 + 主流程冒烟测试 fail-fast。

**清零**：cmd P1.1 / cmd P1.2 / cmd P1.3

| 子项 | 状态 | 证据 | 修复方向 |
|---|---|---|---|
| **CLI-SECONDARY-HELP-01** | ⬜ 待处理 | `cmd/gocell/app/dispatch.go:84` + `cmd/gocell/app/check.go:46` + `cmd/gocell/app/scaffold.go:64` | 二级命令统一支持 `-h` / `help`，不再当 subtype 解析；修正 dispatch 顺序 + 文案 |
| **CLI-UNIMPL-HIDE-01** | ⬜ 待处理 | `cmd/gocell/app/dispatch.go:74` + `cmd/gocell/app/generate.go:31` | `not implemented` 命令从主帮助移除或显式 `[experimental]` 标注；运行时 `exit 64` + 明确指引 |
| **COREBUNDLE-MAINTEST-FAIL-FAST-01** | ⬜ 待处理 | `cmd/corebundle/main_test.go:105,110` | 用可控 listener（`net.Listen("tcp", "127.0.0.1:0")` 注入）替代 :0 端口推断；bind 错误不再被白名单吞掉，断言关键装配里程碑 |

---

## §3 既有 PR 扩充（3 处，不增 PR 数；已完成 1 条）

| 既有 PR | 状态 | 新增子项 | 增工时 |
|---|---|---|---|
| **PR-A53** BOOTSTRAP-LISTENER-SLICE-POLISH | ⬜ 待处理 | **cells P1.5 REPO-HEALTHCHECKER-01**：`cells/configcore/cell.go:204` + `cells/auditcore/cell.go:191` 的 HealthCheckers 主要接 outbox，未纳入关键 repo 健康探针；configcore/auditcore repo 接入 cell HealthCheckers（与 PR-CFG-1 PG relay probe 同主题，同 PR） | +2h |
| **PR-A41** BOOTSTRAP-STRUCT-DECOMPOSE | ✅ 已完成（PR #302） | **cells P1.3 CELL-API-PG-POOL-DECOUPLE-01**：`cells/configcore/cell.go:157` + `cell_init.go:34` Cell 公共 API 暴露 `*adapterpg.Pool` 类型；composition root 装配责任回收到 module 层（与 G1 SharedPGPool 设计反思同主题）| +3h |
| **PR252-F2** COMMAND-SWEEPER-PRODUCTION-GOVERNANCE | ⬜ 待处理 | **kernel P1.4 SWEEPER-OBSERVABLE-01**：`kernel/command/sweeper.go:94,140` onError 默认兜底（slog.Error）+ 并发度按 `groups × capacity × cost` 而非 finding 数计算 | +2h |

---

## §4 排期建议（待用户确认）

```
Sprint 优先批（🔴 发布前必做，~24h ≈ 3d）：
  ├─ PR-SEC-1 FAIL-CLOSED-DEFAULTS         (Cx3, ~12h) 与 PR-A46/A53 同 batch
  └─ PR-CONTRACT-INTEGRITY                 (Cx3, ~12h) PR-CFG-I 之后串行

正确性 / DX 批（🟡 v1.0 GA 前，~28h ≈ 3.5d）：
  ├─ PR-LIFECYCLE-ROBUSTNESS               (Cx3, ~12h)
  ├─ PR-TEST-DEPTH                         (Cx3, ~10h)
  └─ PR-V1-EVOLVE-ADR                      (Cx2, ~3h)   决策前置：方向 A vs B

API/CLI 一致性批（🟡 可延后，~13h ≈ 1.5d）：
  ├─ PR-API-CONSISTENCY                    (Cx3, ~8h)
  └─ PR-CLI-CONSISTENCY                    (Cx2, ~5h)

既有 PR 顺带（不增 PR 数，+7h）：
  ├─ PR-A53  += cells P1.5 repo HealthChecker      (+2h)
  ├─ PR-A41  += cells P1.3 PG pool 不暴露          (+3h)
  └─ PR252-F2 += kernel P1.4 sweeper onError       (+2h)
```

**总增量**：7 个新 PR + 3 个既有 PR 扩充 = **~72h（~9 工作日）**

---

## §5 待更新的 plan 文件改动（待用户确认后执行）

### 5.1 `docs/plans/202604252100-026-post-v1.0-cleanup-plan.md`

**新增 Wave 12 — 安全 fail-closed + 契约执行闭环（发布前硬约束，~24h）**：
- 新增 PR-A65 SEC-1 FAIL-CLOSED-DEFAULTS
- 新增 PR-A66 CONTRACT-INTEGRITY

**新增 Wave 13 — 生命周期 + 测试深度 + API 一致性（v1.0 GA 前，~28h）**：
- 新增 PR-A67 LIFECYCLE-ROBUSTNESS
- 新增 PR-A68 TEST-DEPTH
- 新增 PR-A69 V1-EVOLVE-ADR
- 新增 PR-A70 API-CONSISTENCY
- 新增 PR-A71 CLI-CONSISTENCY

**Wave 8 PR-A53 段扩充**：+ cells P1.5 REPO-HEALTHCHECKER-01（4h → 12h，含 PR-CFG-1 + DEFER-1 + 本扩充）

**长期占位 PR-A41 段扩充**：+ cells P1.3 CELL-API-PG-POOL-DECOUPLE-01（1d → 1d + 3h）

**头部表 + 工时合计同步刷新**：26 PR → **33 PR**；剩余 ~72h → **~144h（~18 工作日）**

> **注**：用户曾在 026 plan 头部规则中规定「不允许新增 PR-A65+ 编号——超出 26 个 PR 的项必须证明属于本 plan 漏网，否则归 027 plan 或 backlog Won't-do」。本次 31 条 P1 中 26 条由分层全仓审查发现（非单 PR review），属于"plan 漏网"——需用户确认是否突破 PR-A65+ 上限，或新建 027 plan 容纳。

### 5.2 `docs/plans/202604260058-l4-virtual-taco.md`

**PR-CFG-I.X2 段扩充**（可选）：把 contracts P1.1（已写明）+ contracts P1.4 部分搬到 X2（archtest 同主题）—— 但更建议保留独立 PR-CONTRACT-INTEGRITY，因 X2 已 12h 接近上限。

**保留 backlog 第 3 条**（如 ADR 选方向 A）：FMT-RESPONSE-STRICT-01 规则需 PR-V1-EVOLVE-ADR 之后回退/分裂。

### 5.3 `docs/backlog.md`

**26 条新 P1 行**按 §2 子项 ID 登记（每条一行 + 工时 + 文件 + 来源指向 `bak/20260426-layered-six-role-review/0X-{layer}.md`）

**已登记 5 条不动**（§1 表已说明现归属）

**3 条既有 PR 扩充**在对应 backlog 行加注「已纳入 PR-AXX 扩充」

---

## §6 决策点（用户确认后批量回灌）

1. **PR-A65+ 编号上限突破**？还是新建 **027 plan** 容纳 7 个新 PR？
2. **PR-V1-EVOLVE-ADR 方向**：A（响应放宽 + 治理规则分输入/输出）vs B（保持 strict + 更新 api-versioning.md 限制 v1 只能加新端点不能加新字段）？方向 A 与 PR-CFG-E #278 落地相反但更对齐业界。
3. **PR-CONTRACT-INTEGRITY 是否拆**？12h 含 5 子项跨 pkg/contracts/cells 三目录；可拆为 INTEGRITY-A（pkg + contracts，~7h）+ INTEGRITY-B（cells 漂移修，~5h），但 review 暴露面拆开后两 PR 都偏小。
4. **既有 PR 扩充方式**：直接更新 plan 文件中 PR-A53/A41/252-F2 段，还是各开 follow-up PR？建议直接扩 plan，避免 PR 数膨胀。
