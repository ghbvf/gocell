# GoCell Backlog

> **单源 backlog** — 按 14 capability units 主轴组织。  
> 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md) §1  
> 历史归档：[`docs/backlog/archive/`](backlog/archive/)
>
> 基线：`origin/develop @ 18a06ab7`（2026-05-07）

---

## Schema

每个 capability 章节一张表，每条 item 一行：

| 列 | 取值 | 说明 |
|---|---|---|
| ID | 沿用旧值；新建项 `<CAP_NUM>-<DOMAIN>-<NNN>` | 唯一 |
| 描述 | `**标题** — 现状: ...; 修复方向: ...`；次要能力末尾 `(also: cap-XX)` | 主内容 |
| Type | `feat` / `bug` / `debt` / `refactor` / `arch-opt` / `doc` / `test` / `fu` | `arch-opt` = "架构优化" |
| P/Cx | 例 `P1/Cx2`；DONE 行可填 `—` | Priority + Complexity 合一列 |
| Flag | 🔴 硬约束（即"发布阻塞项"）/ 🟠 条件延后 / 🟡 可延后 / 🟢 已纳入 plan / ✅ 已完成 | 状态由 Flag 编码：✅ = DONE 待人工归档；其余视为 OPEN |
| Trigger | 仅 Flag=🟠 必填 | 触发条件文本 |
| Files | ≤ 3 个 | 主要涉及文件 |
| Source | PR# / review 报告路径 / issue# | 来源 |

**跨域决策**：(1) 主代码改动落处 → primary；(2) 平手则 contract owner cell 所属 capability；(3) 还平手按 `cells > runtime > kernel > tools` 优先级；(4) 跨 ≥ 4 cap 且无明确 owner 才进 `cap-x-cross`。次要 capability 在描述里写 `(also: cap-XX)`，物理只在 primary 章节出现一次。

**归档**：人工。Flag=✅ 留主表至人工迁 [`archive/`](backlog/archive/)（按季度命名 `2026-q2-completed.md`）；WONTFIX 立即移 archive + 理由必填。

---

## cap-01: Cell 声明与生命周期

> 主要包：`kernel/cell` + `assembly` + `lifecycle` + `worker` + `runtime/worker`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| B2-K-02 | **Kernel Must*/error-first 混用** — 现状: `MustNewAuthJWT` 等 Must 系列与 error-first 构造器混用，composition root 残留 panic；修复: 生产路径改 error-first，Must 仅 test-only/cmd 顶层 | bug | P1/Cx3 | 🟡 | — | `kernel/wrapper/handler.go` + `kernel/cell/auth_plan.go` + `pkg/contracttest/` | backlog2 §2 B2-K-02 |
| B2-C-03 | **Cell.Init 泄漏基础设施类型** — 现状: configcore Cell 公共 API 暴露 `*adapterpg.Pool`；修复: 装配责任回收到 module 层（与 G1 SharedPGPool 同主题）| arch-opt | P1/Cx3 | 🟡 | — | `cells/configcore/cell_init.go` + `cell.go` | backlog2 §4 B2-C-03 |
| B2-PROVISIONER-MUTEX-REVIEW | **Provisioner mutex 清理 review** — 现状: A26-R1 已删 initialadmin，但 provisioner mutex 残留；修复: PG adapter 落地后审视是否仍需 mutex | refactor | P2/Cx1 | 🟠 | PG adapter for accesscore | `cells/accesscore/internal/adminprovision/provisioner.go` | backlog2 §13 |

---

## cap-02: 元数据解析与治理

> 主要包：`kernel/metadata` + `governance` + `verify` + `depgraph` + `tools/archtest` + `tools/generatedverify`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| P1-5 | **METADATA-PERF-BENCH-01** — 现状: 缺 `BenchmarkParseFS_500Files` 性能基准；修复: 加 bench + 评估 goccy/go-yaml 单次解码迁移成本 | test | P1/Cx3 | 🟡 | — | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| KERNEL-CONTRACTSPEC-CONTRACTMETA-DUAL-DEF-01 | **Contract 双源定义** — 现状: `kernel/wrapper.ContractSpec` 与 `kernel/metadata.ContractMeta` 双源；修复: K#04 PR-4 codegen 落地时合一 | arch-opt | Cx3 | 🟠 | K#04 PR-4 codegen 迁移 | `kernel/wrapper/` + `kernel/metadata/` | systems layer review |
| KERNEL-INTERNAL-DAG-GUARD-01 | **kernel 反向 import 守卫** — 现状: 缺 archtest 守 kernel 反向 import；修复: 引入新依赖时一并加 DAG 守卫 | arch-opt | Cx2 | 🟠 | kernel 出现新反向引用 | `tools/archtest/` | systems layer review |
| ASSEMBLY-SCHEMA-MINIMUM-VIABLE-01 | **Assembly schema 最小可用** — 现状: AssemblyMeta 缺 owner + maxConsistencyLevel + deployTemplate enum；修复: 加 2 个 assembly 时一并落 | arch-opt | P1/Cx2 | 🟠 | 加第 2 个 assembly | `kernel/metadata/types.go` + governance + assembly.yaml | systems-layer-07 §P1-1+2 |
| SHARED-ERROR-SCHEMA-GENERATION-01 | **共享 error schema 单源** — 现状: 4 份 mirror 人工同步；修复: canonical → make generate 派生 examples/testdata | arch-opt | P2/Cx2-Cx3 | 🟡 | 下次 envelope schema 变更 | `contracts/shared/errors/` + `tests/contracttest/testdata/` | PR#396 review |
| KERNEL-DEPGRAPH-OUT-EVAL-01 | **Depgraph out evaluation** — 现状: depgraph 只 in-eval；修复: 加 out-eval 路径 | arch-opt | Cx3 | 🟠 | 第 3 个 depgraph 消费方 | `kernel/depgraph/` + `runtime/` | PR#357 |
| CELLS-SLICE-MULTI-VERB-DECOMPOSE-01 | **Slice 多 verb 拆分** — 现状: auditcore/configcore 多 slice 跨 verb；修复: 加 4+ cell 时拆分 | arch-opt | Cx3 | 🟠 | 4+ cell 加入 | `cells/auditcore` + `configcore/` | systems layer review |
| M2-LIFECYCLE | **CELL-SLICE-LIFECYCLE-FIELD-01** — 现状: cell/slice 缺生命周期相位声明；修复: cell.yaml/slice.yaml 加 `lifecycle` 字段 (experimental/candidate/asset/maintenance/retired) + governance 校验状态转移合法性 + 运行时通过 Aggregator 接口暴露当前相位（差距由消费方计算）(also: cap-13) | feat | P2/Cx3 | 🟠 | M1 落地 | `kernel/metadata/types.go` + `kernel/governance/` + `kernel/healthz/` | ADR-202605041430 M2 |
| M3-RULE-ENGINE | **GOVERNANCE-RULE-ENGINE-DATA-DRIVEN-01** — 现状: governance 64 规则散在 Go 代码；修复: `kernel/governance/engine.go` 唯一执行体 + `kernel/governance/rules/*.yaml` 数据化（5 槽位 detect/evidence/next/level/harvest）+ `next-action` 五级 (autofix/suggest/advisory/block/escalate) + 规则带 `metric` 距离函数 + 修 ADV-05 SeverityError 错分 | refactor | P2/Cx3 | 🟡 | — | `kernel/governance/engine.go` (新) + `kernel/governance/rules/*.yaml` (新) | ADR-202605041430 M3 |
| G-1 | **FMT-11 dynamic-status-field 隔离** — 现状: 动态状态字段（readiness/risk/blocker）漏入非 status-board 文件，元数据被污染；修复: governance 加 FMT-11 严格隔离 | doc | P2/Cx2 | 🟡 | 出现元数据污染或非法 contract 引用 | `kernel/governance/` | backlog_later §1 |
| G-2 | **TOPO-07 actor.maxConsistencyLevel 校验** — 现状: parser 已解析 actor.maxConsistencyLevel 但校验阶段不阻断；修复: governance 加 TOPO-07 阻断 | bug | P2/Cx2 | 🟡 | 同 G-1 | `kernel/metadata/parser.go` + governance | backlog_later §1 |
| G-4 | **Deprecated contract 引用阻断** — 现状: deprecated 仅 warning；修复: 升 P1 break build | arch-opt | P2/Cx2 | 🟡 | v1.1 启动 | `kernel/governance/` | backlog_later §1 |
| G-6 | **Assembly boundary.yaml 一致性校验** — 现状: 派生文件无校验；修复: 加生成系一致性校验 | doc | P3/Cx1 | 🟡 | v1.1 启动 | `kernel/governance/` | backlog_later §1 |
| DURABLE-TYPE-01 | **L2/L3 持久化级别静态保护** — 现状: 类型抹除让 L2/L3 检测退化为启动期 panic；修复: 探索类型系统层面静态编译保护（仓储级能力推断） | arch-opt | P2/Cx3 | 🟡 | v1.1 启动 | `kernel/metadata/` + `kernel/persistence/` | backlog_later §6 |
| B2-K-05 | **Metadata parser error 路径泄漏** — 现状: parse error 含 fs 内部路径，低强度信息泄露；修复: error 双通道 (public 仅 cell/slice ID + 字段路径，internal slog 保留 fs path) | bug | P2/Cx2 | 🟡 | — | `kernel/metadata/parser.go:190,202` | backlog2 §2 B2-K-05 |
| B2-K-07 | **Contracttest undeclared ref no-op** — 现状: `MustValidateRequest("not-declared", ...)` 静默 return，key 写错时假通过；修复: 未声明 key 改 `t.Fatalf` | bug | P1/Cx1 | 🟡 | — | `pkg/contracttest/contracttest.go:170,189` | backlog2 §2 B2-K-07 |
| B2-T-07-FU-3 | **K04-CELLGEN-CONTRACTSPEC-CLIENTS** — 现状: cellgen 不派生 contract.clients；修复: 加派生（A5 follow-up） | arch-opt | Cx2 | 🟡 | cellgen 升级窗口 | `tools/codegen/cellgen/` | backlog2 §8 A5 follow-up |

---

## cap-03: Contract 注册与发现

> 主要包：`kernel/wrapper` + `kernel/registry` + `pkg/contracts`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| P1-8 | **DEVICE-LIST-API** — 现状: `cells/devicecell/slices/devicelist/` 缺；修复: 新建 slice + `GET /api/v1/devices` 分页 + contract + contract_test | feat | P1/— | 🟡 | — | `cells/devicecell/slices/devicelist/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| B2-C-08 | **Configcore event decoder 严格** — 现状: cell event consumer DisallowUnknownFields，PR-V1-EVOLVE-ADR 之后应放宽；修复: 关闭 DisallowUnknownFields | bug | P1/Cx2 | 🟡 | — | `cells/configcore/internal/events/config_events.go:82` | backlog2 §4 B2-C-08 |
| B2-T-01 | **Config rollback 乐观锁缺** — 现状: rollback 无版本检查；修复: 加乐观锁版本号（与 cap-09 P3-TD-12 同根源，本条聚焦 contract 层声明）| bug | P1/Cx2 | 🟡 | 与 cap-09 P3-TD-12 协同 | `contracts/http/config/rollback/v1/contract.yaml` + `cells/configcore/internal/ports/config_repo.go:23-25` | backlog2 §8 B2-T-01 |
| B2-T-04 | **Contract userId 风格混用** — 现状: payload schema 字段命名混用 userId/UserID；修复: 统一 camelCase | refactor | P2/Cx2 | 🟡 | — | `contracts/event/user/created/v1/payload.schema.json:6` | backlog2 §8 B2-T-04 |

---

## cap-04: HTTP 入站处理

> 主要包：`runtime/http/{router,middleware,health,devtools}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| A26-R3 | **SETUP-PATH-NAMESPACE-POLICY-01** — 现状: 顶级 `/api/v1/setup/` 与 per-Cell 入口规则未明文；修复: 在 api-versioning.md 写明 | doc | Cx1 | 🟡 | — | `.claude/rules/gocell/api-versioning.md` | PR#247 round-2 N-01 |
| HTTPUTIL-WRITEERRORBODY-DOUBLE-MARSHAL | **错误响应双重 JSON marshal** — 现状: writeErrorBody marshal+unmarshal+encode 三次；修复: errcode.MarshalJSON 原生支持 envelope 注入 | bug | P3/Cx1 | 🟡 | HTTP 错误成 hot path | `pkg/httputil/response.go` + `pkg/errcode/errcode.go` | PR #391 review round-2 |
| PR391-HEALTH-VERBOSE-REDACTION-01 | **Readyz verbose redaction** — 现状: verbose 503 dependency error 仅 truncate，可能含 secret；修复: 走 `pkg/redaction` + 4 通道分明 | arch-opt | P1/Cx2 | 🟠 | 发布前安全收口 | `runtime/http/health/` + ADR | PR#391 review security |
| PR392-FU-RATE-LIMITER-DISTRIBUTED | **BOOTSTRAP-RATELIMIT-DISTRIBUTED-01** — 现状: in-memory token bucket per pod；修复: 出现暴力枚举威胁时引入 Redis-backed | arch-opt | P3/Cx3 | 🟡 | bootstrap mode + 多 pod | `adapters/ratelimit/` + `cmd/corebundle/access_module.go` | PR #392 ADR §D10 |
| PR237-T1 | **Listener timeout pattern** — 现状: timeout 配置分散；修复: 抽统一 listener config | arch-opt | Cx2 | 🟡 | — | `runtime/http/` | PR#237 |
| PR237-PM5 | **DUAL-LISTENER-DEPLOYMENT-GUIDE-01** — 现状: 缺双 listener 部署章节；修复: 新增 `docs/operations/dual-listener-deployment.md` | doc | Cx2 | 🟡 | — | `docs/operations/` | PR #237 round-2 PM-05 |
| PR237-PM7 | **EXAMPLE-INTERNAL-LISTENER-COMMENT-01** — 现状: examples/*/main.go 双 addr 缺注释；修复: 加注释或 `WithHTTPInternalDisable` | doc | Cx1 | 🟡 | — | `examples/*/main.go` | PR #237 round-2 PM-07 |
| LISTENER-API-SPEC-01 | **Listener API spec 化** — 现状: listener 选项散在代码；修复: contracts 化声明 | arch-opt | Cx2 | 🟡 | — | `contracts/http/` | PR#237 |
| ROUTE-ERROR-POLICY-01 | **Route error policy 统一** — 现状: 3+ route family 错误处理不一；修复: 定义共享 policy | arch-opt | Cx3-Cx4 | 🟠 | 3+ route 家族出现 | `runtime/http/` | systems review |
| PR-CI-5-FU-WEBSOCKET-ORIGIN-CONTRACT | **WEBSOCKET-ORIGIN-CONTRACT-TRIM-NORMALIZE-01** — 现状: Validate trim 仅判断未规范化；修复: trim 写回 cfg + 文档统一裸 host vs 完整 origin | arch-opt | P2/Cx1-Cx2 | 🟢 | — | `adapters/websocket/handler.go` + integration_test.go | 029 D7 / via /fix PR#335 |
| T8-B | **PATH-PARAM-PREVALIDATE** — 现状: handler-side path param 校验分散；修复: 路由前预校验 helper | arch-opt | — | 🟠 | 安全审查触发 | `runtime/auth/` + `pkg/httputil/` | PR-A45 |
| T4 | **CB-RESILIENCE-PACKAGE-01** — 现状: Allower / CircuitBreakerRetryAfter 在 `runtime/http/middleware`；修复: 迁到 `runtime/resilience/circuitbreaker/` 独立包 (also: cap-x-cross) | refactor | — | 🟠 | 出现第 2 个非 HTTP CB 消费方 | `runtime/http/middleware/` + `runtime/resilience/circuitbreaker/` (新) | T4 |
| WM-32 | **mTLS 中间件** — 现状: 缺；修复: 加 TLS 构建器 + HTTP 证书提取钩子（折中：大规模环境 mTLS 卸载在 K8s/Service Mesh 解决，框架仅提供构建器） | feat | P2/Cx2 | 🟡 | V1.1 启动 | `runtime/http/middleware/` | backlog_later §7 WM-32（4/6 票）|
| B2-C-07 | **Configclient 不可恢复错误走 requeue** — 现状: 不可恢复 HTTP 错误也走 retry/requeue；修复: 区分 4xx/5xx 决定 ack/requeue/reject | bug | P1/Cx2 | 🟡 | — | `cells/accesscore/internal/adapters/http/configclient.go:91` + `cells/accesscore/slices/configreceive/service.go` | backlog2 §4 B2-C-07 |
| B2-X-04 | **Health listener 默认 loopback** — 现状: 默认绑 0.0.0.0（即使 healthListener）；修复: 默认 loopback，显式 opt-in 暴露 | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `cmd/corebundle/shared_deps.go:461` | backlog2 §7 B2-X-04 |
| B2-T-08 | **Config publish 失败码声明不完整** — 现状: contract 缺部分失败码声明；修复: 补 4xx/5xx 完整声明 | bug | P2/Cx1 | 🟡 | — | `contracts/http/config/publish/v1/contract.yaml` | backlog2 §8 B2-T-08 |

---

## cap-05: 身份认证 (Authn)

> 主要包：`runtime/auth` + `auth/refresh` + `auth/refresh/memstore` + `auth/config`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| AUTH-SERVICETOKEN-INVALID-MAC-FLAKE-01 | **InvalidMAC test 1/256 偶发失败** — 现状: `badToken[:len-2]+"ff"` 当原值就是 ff 时变 no-op；修复: XOR 翻转或 00/ff 互换 | test | P3/Cx1 | 🟡 | — | `runtime/auth/authenticator_test.go` | PR #301 |
| B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01 | **B5 follow-up PG runtime wiring + archtest 类型化** — 现状: corebundle 仍走 `WithInMemoryDefaults`；修复: B2 落 PG SessionRepository 后切真实 PG + archtest 升 packages-aware | refactor+test | P1+P2/Cx2+Cx3 | 🟠 | B2 落地 PG SessionRepository | `cmd/corebundle/access_module.go` + `cells/accesscore/cell_init.go` + `tools/archtest/` | PR#399 review L2 |
| ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 | **ACCOUNT-LOCKOUT-AUTO-LOCK-01** — 现状: sessionlogin 无失败次数累计 + 阈值 + auto-lock；修复: 完整业务设计 + PG schema + journey harness | feat | Cx3 | 🔴 | — | `cells/accesscore/slices/sessionlogin/` + user repo + integration test | PR-A63 复核 |
| CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 | **identitymanage 一致性等级误标** — 现状: 标 L0 实为 L1；修复: 校正 slice.yaml | arch-opt | Cx1 | 🔴 | — | `cells/accesscore/slices/identitymanage/slice.yaml` | systems layer review |
| OIDC-FAIL-FAST-DISCOVERY-01 | **OIDC discovery fail-fast** — 现状: discovery 错误不 fail-closed；修复: 引入 OIDC 时落地 | bug | Cx2 | 🟠 | 首个 prod OIDC 部署 | `adapters/oidc/` | systems layer review |
| OIDC-JWKS-ROTATION-WORKER-01 | **OIDC JWKS 轮转 worker** — 现状: 缺 background fetch；修复: 与 fail-fast 一同落地 | feat | Cx2 | 🟠 | 与 OIDC discovery 同 | `adapters/oidc/` | systems layer review |
| PR-A8-RESIDUAL | **Vault K8s auth E2E** — 现状: Vault K8s auth 实现已落，缺真 K8s e2e；修复: 跑 testcontainers k8s 验证 | arch-opt | Cx2 | 🟡 | — | `adapters/vault/` | PR#305 |
| PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST | **登录 durable TX atomicity 集成测试** — 现状: 仅单元测；修复: PG session repo 落地后补 service-level test | test | Cx2 | 🟠 | PG session repo 落地 | `cells/accesscore/slices/sessionlogin/` | PR#338 |
| PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP | **AUTH-FAIL-CLOSED-DOC-CLEANUP-01** — 现状: nonce.go docstring + archive quickstart 未跟 PR-CFG-I 更新；修复: 补 deprecation banner | doc | P3/Cx1 | 🟡 | — | `runtime/auth/nonce.go` + `docs/archive/specs/201-wm2-key-rotation/quickstart.md` | PR#338 round-1 |
| PR267-FU-AUTHTEST-INTERNAL | **Auth test 内部化** — 现状: testHelpers 暴露过多；修复: internal package | arch-opt | Cx1 | 🟡 | — | `cells/accesscore/` | PR#267 |
| PR267-FU-ROLE-PREFIX-ADR | **Role prefix ADR** — 现状: role 命名前缀约定无 ADR；修复: 写 ADR | doc | Cx1 | 🟡 | — | `docs/architecture/` | PR#267 |
| X2 | **WM-35 BFF handler cookie session 接入** — 现状: BFF 无 cookie session；修复: 接入 SecureCookie | feat | P3/— | 🟡 | — | BFF + `runtime/auth/` | 长期 roadmap |
| X3 | **WM-36 SecureCookie key rotation** — 现状: 无密钥轮转；修复: 接入 rotation worker | feat | P3/— | 🟡 | — | `runtime/auth/` | WM-35 后续 |
| X5 | **P3-TD-11 accesscore domain 拆分** — 现状: domain 包过大；修复: User/Session/Role 拆分 | refactor | P3/— | 🟡 | X1 落地后 | `cells/accesscore/internal/domain/` | 历史 Batch 8 |
| X12 | **REFRESH-IDLE-EXPIRE-01** — 现状: 无 idle expire 滑动窗口；修复: 加 `idle_expires_at` 列 + Policy.MaxIdle | feat | P3/Cx2 | 🟠 | PR-A29 已合可启动 | `runtime/auth/refresh/types.go` + `adapters/postgres/` + migration | Zitadel 双过期对标 |
| X13 | **REFRESH-PARTITION-01** — 现状: 批量 DELETE GC；修复: `expires_at` range 分区 + DROP PARTITION (also: cap-10) | feat | P3/Cx2 | 🟠 | 生产流量达阈值 | migration + ops runbook | 通用 PG 模式 |
| X14 | **REFRESH-GRACE-COUNTER-01** — 现状: 无重用次数限制；修复: `first_used_at` + `used_times` 列 | feat | P3/Cx2 | 🟠 | PR-A29 已合可启动 | `adapters/postgres/refresh_store.go` + migration | Hydra COALESCE 对标 |
| T1 | **AUTH-PROVIDER-EXPORT-01** — 现状: `authProvider` interface unexported；修复: 移出 `runtime/bootstrap` | arch-opt | — | 🟠 | 第 2 个 auth provider cell | `runtime/bootstrap/` | T1 |
| T2 | **AUTH-ISSUE-OPTIONS-01** — 现状: `JWTIssuer.Issue()` 5 参数；修复: IssueOptions struct | arch-opt | — | 🟠 | Issue() 第 5 个参数 | `runtime/auth/` | T2 |
| T5 | **AUTH-SIGNER-01** — 现状: SigningKeyProvider 返回 `*rsa.PrivateKey`；修复: 改 `crypto.Signer` 支持 HSM/KMS/EC | arch-opt | — | 🟡 | caller 需 HSM/KMS | `runtime/auth/` | T5 |
| C-AC7 | **JWT jti claim 支持** — 现状: 缺 jti，单 token 无法黑名单撤销；修复: Issue() 加 jti + jti 黑名单存储 | feat | P2/Cx2 | 🟡 | 出现单 token 撤销需求 | `runtime/auth/` | backlog_later §6 C-AC7 |
| P3-TD-10 | **TOCTOU 竞态修复** — 现状: Phase 2 #54 session TOCTOU 未修；修复: Redis 分布式锁 + 持久化 session 稳定后处理 (also: cap-11) | bug | P2/Cx3 | 🟠 | post-v1.0 + Redis distlock 稳定 + PG session repo | `cells/accesscore/` | tech-debt-registry P3-TD-10 |
| P4-TD-03 | **IssueTestToken HS256 dead code** — 现状: 测试 helper 仍保留 HS256 路径，JWTVerifier 全拒；修复: 删 dead code 防误用 | refactor | Cx1 | 🟡 | — | `runtime/auth/` (test helper) | tech-debt-registry P4-TD-03 |
| SECURECOOKIE-AEAD-NEG-01 | **SecureCookie AEAD 负向测试** — 现状: AEAD 失败路径无测试；修复: 截断/伪造/边界长度/解密失败类型断言 (`errors.Is(err, ErrAEADAuthFailed)`) | test | Cx2 | 🟡 | v1.0 GA 前 | `pkg/securecookie/securecookie_test.go` | backlog1 §2.5 |
| B2-C-02 | **SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT** — 现状: setup 端点常驻 Public，未初始化窗口可被匿名首管抢注；修复: 移到 `/internal/v1/setup/` (service-token only) + contract `lifecycle: bootstrap`，或 1 次性 bootstrap token | feat | P0/Cx3 | 🔴 | — | `cells/accesscore/cell_routes.go:73` + `slices/setup/handler.go:46-58` + `contracts/http/auth/setup/admin/v1/contract.yaml:5` | backlog2 §1 B2-C-02 |
| B2-A-08 | **PG refresh store ambient tx 混用** — 现状: 部分方法用 ambient tx，部分用显式 BeginTx；修复: 统一显式事务边界 | arch-opt | P1/Cx3 | 🟡 | — | `adapters/postgres/refresh_store.go:141,190,227` | backlog2 §5.1 B2-A-08 |
| B2-A-09 | **PG refresh 拒绝耗时侧信道** — 现状: 拒绝路径耗时不一致，可侧信道推断；修复: 加常量时间常量比较 | bug | P1/Cx3 | 🟡 | 安全审查触发 | `adapters/postgres/refresh_store.go:221,295,330` | backlog2 §5.1 B2-A-09 |

---

## cap-06: 授权决策 (Authz)

> 主要包：`runtime/auth` (authz/policy)

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| T3 | **DEVICE-ENQUEUE-RBAC** — 现状: HandleEnqueue 无设备维度鉴权；修复: 加设备粒度策略 | feat | — | 🟠 | 多租户 operator | `cells/devicecell/` | T3 |
| T11 | **ADMIN-ROLE-DEDUP** — 现状: admin role 字符串散在多处；修复: 抽 const 单源 | arch-opt | — | 🟠 | role 命名漂移出现 | `pkg/auth/` + `cells/` | T11 |
| B2-C-06 | **SessionLogout consumer action 无验证** — 现状: consumer.go 接受任意 action 字段；修复: 加 action enum 校验 | bug | P1/Cx2 | 🟡 | — | `cells/accesscore/slices/sessionlogout/consumer.go:69` | backlog2 §4 B2-C-06 |
| B2-T-02 | **RBACASSIGN event contract waiver expiry** — 现状: contract test waiver 已设置过期；修复: waiver 到期前补真实 contract 实现 | bug | P1/Cx2 | 🟠 | waiver 到期前 | `cells/accesscore/slices/rbacassign/contract_test.go:84,93` | backlog2 §8 B2-T-02 |
| B2-T-05 | **Internal contract external actor drift** — 现状: contract 声明 external actor 但实际是 internal；修复: 校正 boundary.yaml | arch-opt | P1/Cx2 | 🟡 | — | `contracts/http/auth/role/{assign,revoke}/v1/contract.yaml` + `boundary.yaml` | backlog2 §8 B2-T-05 |
| B2-T-07-FU-1 | **RBACASSIGN accesscore caller wiring** — 现状: production wiring 缺 caller；修复: 接入 caller (A5 follow-up) | arch-opt | Cx2 | 🟠 | production wiring 启动 | `cells/accesscore/slices/rbacassign/contract_test.go` | backlog2 §8 A5 follow-up |
| B2-T-07-FU-2 | **BUILTIN-SERVICE-ROLES 删除 FU** — 现状: scope 派生 builtin role 还在 hard-code；修复: 完全派生（A5 follow-up） | arch-opt | Cx3 | 🟠 | scope 派生工具就绪 | `runtime/auth/principal.go` | backlog2 §8 A5 follow-up |

---

## cap-07: 事务性事件发布 (Outbox Producer)

> 主要包：`kernel/outbox` + `runtime/outbox` + `adapters/postgres` (outbox table)

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR341-FU-OUTBOXTEST-CLOSE-BUDGET-COVERAGE | **OUTBOXTEST-CLOSE-BUDGET-COVERAGE-01** — 现状: conformance suite 仍裸调 `sub.Close(ctx)`；修复: 全部走 closeWithBudget 或 godoc 强约定 | test | P2/Cx1 | 🟡 | — | `kernel/outbox/outboxtest/conformance.go` | PR #341 round-1 |
| P4-TD-04 | **ordercell L2 事务性 outbox** — 现状: ordercell 声明 L2 但用 publisher.Publish 而非事务性 outbox.Writer；修复: Init 强制 outboxWriter 注入 + 替换 Publish | bug | P2/Cx2 | 🟡 | v1.1 启动 | `examples/todoorder/cells/ordercell/` | tech-debt-registry P4-TD-04 |
| AUDITAPPEND-L2-FAILURE-PROOF-01 | **AuditAppend L2 失败注入测试** — 现状: 缺 PG-level 失败注入证明；修复: testcontainer + 故意 fail outbox writer 验证 DB 写成功 + outbox 失败 → 事务真回滚 | test | P1/Cx3 | 🟡 | v1.0 GA 前 | `cells/auditcore/slices/auditappend/outbox_test.go` | backlog1 §2.5 |

---

## cap-08: 异步事件消费 (Subscriber+Claimer)

> 主要包：`kernel/{outbox,idempotency}` + `runtime/eventrouter` + `adapters/{redis,rabbitmq}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| RELAY-RETRYDELAY-TABLE-TEST-01 | **Relay retry delay 表驱动测试** — 现状: retry delay 路径覆盖单一；修复: 加 table-driven test | test | Cx2 | 🟡 | — | `adapters/rabbitmq/` | — |
| CELL-CONSUMER-EXTRA-TOPICS-OPTION-01 | **Cell consumer extra topics option** — 现状: cell 无法订阅同 cell 外的 extra topics；修复: 加 Option | feat | Cx3 | 🟡 | — | `kernel/cell/` | GitHub #303 |
| KERNEL-REPLAY-01 | **kernel/replay 投影重算** — 现状: 缺 CQRS Projection rebuild；修复: 新建 replay 包 + 依赖 Consumer 模型稳定后实现 | feat | P3/Cx3 | 🟡 | Consumer 模型稳定 + 业务出现 CQRS rebuild 需求 | `kernel/replay/` (新) | backlog_later §2 |
| KERNEL-RECONCILE-01 | **kernel/reconcile L3 收敛循环** — 现状: 缺 Reconciler 模式；修复: 新建 reconcile 包 | feat | P2/Cx3 | 🟡 | L3 业务出现 | `kernel/reconcile/` (新) | backlog_later §2 |
| WM-18 | **延迟消息原语** — 现状: 缺 TTL；修复: RMQ x-delayed-message 插件绑定 + 测试桩支持（运维成本拉升，等 Outbox 稳定后探索） | feat | P2/Cx2 | 🟡 | V1.1 启动 + Outbox 彻底稳定 | `adapters/rabbitmq/` + outbox | backlog_later §7 WM-18（3/6 票）|
| B2-K-06 | **EventRouter consumerGroup 与 cellID 混淆** — 现状: `Subscription.CellID = h.consumerGroup`，下游 metrics/日志属性自相矛盾；修复: 显式拆分 `CellID` 与 `ConsumerGroup` | bug | P2/Cx3 | 🟡 | — | `runtime/eventrouter/router.go:364` | backlog2 §2 B2-K-06 |
| B2-A-14 | **RMQ StopIntake prefetch 未排空** — 现状: StopIntake 后 prefetch 仍在投递；修复: 排空 prefetch 后再退出 | bug | P1/Cx3 | 🟠 | StopIntake 耦合 | `adapters/rabbitmq/subscriber.go:914` | backlog2 §5.2 B2-A-14 |
| B2-A-15 | **RMQ channel 无上限** — 现状: connection.go 创建 channel 无上限；修复: 加 channel cap + reuse pool | bug | P1/Cx3 | 🟠 | 无上限创建出现 | `adapters/rabbitmq/connection.go:171` | backlog2 §5.2 B2-A-15 |
| B2-A-16 | **RMQ publish nack 无告警** — 现状: NACK/超时静默丢；修复: 告警 + 计数 metric | bug | P1/Cx1 | 🟠 | NACK/超时出现 | `adapters/rabbitmq/publisher.go:133,136,143` | backlog2 §5.2 B2-A-16 |
| B2-A-17 | **RMQ EventBus 语义集成测试缺** — 现状: conformance test 不全；修复: 补 publisher/subscriber 全链路 testcontainer 集成测试 | test | P1/Cx3 | 🟡 | — | `adapters/rabbitmq/conformance_test.go:18` | backlog2 §5.2 B2-A-17 |
| B2-A-26 | **Redis idempotency receipt commit/release race** — 现状: receipt commit 和 release 之间有 race；修复: Lua 原子化 | bug | P1/Cx3 | 🟡 | — | `adapters/redis/idempotency.go:136-200` | backlog2 §5.3 B2-A-26 |
| B2-A-27 | **Redis idempotency multi-tenant key 碰撞** — 现状: 缺 KeyNamespace cell prefix（B10 只解决 cluster slot，B11 待办）；修复: 加 cell prefix | bug | P1/Cx3 | 🟡 | 多租户隔离需求 | `adapters/redis/idempotency.go:127-130` | backlog2 §5.3 B2-A-27 |
| B2-C-10 | **Auditappend 全局 mutex 串行化 13 topic** — 现状: 单 mutex 串行化所有 topic 处理；修复: 按 topic/分片细化锁 | bug | P1/Cx3 | 🟡 | 容量/吞吐压力出现 | `cells/auditcore/slices/auditappend/service.go:93,165` | backlog2 §4 B2-C-10 |
| B2-R-B-13-FU-01 | **RMQ-PR379-FU-DOC-DRIFT** — 现状: PR#379 review 留 doc 漂移；修复: 补 docs/guides/ 同步 | doc | P2/Cx1 | 🟡 | — | `adapters/rabbitmq/subscriber.go` + `docs/guides/` | backlog2 §13 |
| B2-R-B-13-FU-02 | **RMQ-SUBSCRIBER-TERMINAL-PROPAGATION** — 现状: PR#379 review 留 terminal 错误传播缺测；修复: 加 propagation test | test | P2/Cx2 | 🟡 | — | `adapters/rabbitmq/subscriber.go:374-378` | backlog2 §13 |

---

## cap-09: 配置加载与热更新

> 主要包：`runtime/config` + watcher + `cells/configcore`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR-CFG-A-DEFER-2 | **ConfigCore L2 divergence** — 现状: L2 与 L1 表项 schema 偏差；修复: 收口 | arch-opt | Cx1 | 🟡 | — | `cells/configcore/` | PR#268 |
| CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 | **ConfigCore 缓存生命周期归属** — 现状: 内存增长信号；修复: 明确 owner + 清理 | arch-opt | Cx2 | 🟠 | 出现内存增长信号 | `cells/configcore/` | systems layer review |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | **ConfigReceive placeholder 清理** — 现状: PR266 metadata-only 后还有 placeholder 残余；修复: 与 PR266 一同 | refactor | Cx2 | 🟠 | 与 PR266 metadata consumer 同 | `cells/accesscore/` | systems layer review |
| PR-CFG-G1-FU6 | **ConfigCore G1 follow-up 6** — 现状: PR-CFG-G1 余项；修复: 单独跟进 | arch-opt | Cx2 | 🟡 | — | `cells/configcore/` | PR-CFG-G1 |
| PR320-FU-CONFIGCORE-CI-NOOP | **ConfigCore CI noop test** — 现状: noop publisher CI 路径未覆盖；修复: 加测 | test | P3/Cx1 | 🟡 | — | `cells/configcore/` | PR#320 |
| PR-CFG-D-FU | **PR-CFG-D follow-up** — 现状: configrepo edge case 残项；修复: 跟进 | arch-opt | Cx2 | 🟡 | — | `cells/configcore/` | PR-CFG-D |
| P3-TD-12 | **configpublish.Rollback 版本校验** — 现状: 缺持久化版本管理；修复: 加版本校验防 rollback 到不存在版本 | feat | P2/Cx2 | 🟠 | post-v1.0 + 持久化版本管理 | `cells/configcore/` | tech-debt-registry P3-TD-12 |
| B2-A-33 | **Redis sentinel env & logvalue 缺** — 现状: sentinel 模式 env 配置不完整 + log value 缺；修复: 补 env 列表 + logvalue 透传 | bug | P2/Cx2 | 🟡 | sentinel 部署 | `cmd/corebundle/redis.go:18-22` + `adapters/redis/client.go:90-104` | backlog2 §5.3 B2-A-33 |
| B2-C-11 | **Configsubscribe tombstone 无 TTL** — 现状: tombstone 永久保留导致内存膨胀；修复: 加 TTL + 定期清理 | bug | P2/Cx2 | 🟡 | — | `cells/configcore/slices/configsubscribe/service.go:29,169` | backlog2 §4 B2-C-11 |

---

## cap-10: 持久化与加密

> 主要包：`kernel/persistence` + `kernel/crypto` + `adapters/{postgres,vault}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| ACCESSCORE-PG-USERS-MIGRATION-01 | **AccessCore PG repository + migration** — 现状: 仅内存；修复: users/roles/role_assignments 表 + UNIQUE on admin role | feat | P1/— | 🔴 | — | `adapters/postgres/accesscore/` | PR #392 v2 review |
| A26-R4 | **SETUP-ORPHAN-E2E-01** — 现状: orphan recovery 仅单元测；修复: PG adapter 落地后真 DB e2e | test | Cx2 | 🟠 | PG adapter for accesscore | `cmd/corebundle/setup_integration_test.go` | PR#247 round-2 N-06 |
| PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE | **TEST-RACE-COVERAGE-ADAPTERS-INTEGRATION-01** — 现状: PG concurrent Up CI 不带 -race；修复: test-race.yml 加 adapters/postgres 路径（评估） | test | P2/Cx3 | 🟡 | — | `.github/workflows/test-race.yml` | PR-V1-PG-STARTUP-HARDEN F5 |
| X1 | **PG-DOMAIN-REPO** — 现状: 5 个 Repository 仅内存；修复: User/Session/Role/Device/Command PG 实现 + 4 migration DDL；联动 RBAC-ASSIGN-LEVEL-UPGRADE/SEED-ROLE-IFACE/AUTH-CACHE 激活 (also: cap-05) | feat | P3/— | 🟡 | — | `adapters/postgres/*` | PR#155 review F4 |
| S14a | **AWS KMS provider** — 现状: 仅 Vault；修复: 加 KMS adapter | feat | — | 🟠 | 云平台部署需求 | `adapters/kms/` (新) | S14a |
| P3-TD-02 | **postgres adapter 覆盖率** — 现状: 测量基准 46.6%（要求 ≥80%）；testcontainers 已实现但 CI 未测量；修复: CI 加 -tags=integration 覆盖率测量（合并 P4-TD-08）| test | P2/Cx2 | 🟡 | — | `adapters/postgres/` + `.github/workflows/` | tech-debt-registry P3-TD-02 + P4-TD-08 |
| P4-TD-11 | **Migrator.Down() v=0 回归测试** — 现状: 已恢复 idempotent no-op 但缺第三次 Down() 测试锁定；修复: 加锁定测 防依赖升级回归 | test | Cx1 | 🟡 | — | `adapters/postgres/migrator_test.go` | tech-debt-registry P4-TD-11 |
| B2-A-11 | **PG constructor error model 混杂** — 现状: refresh_store 等构造器混合 panic + error；修复: 统一 error-first（与 cap-12 STARTUP-ROLLBACK 同主题）| arch-opt | P1/Cx3 | 🟡 | — | `adapters/postgres/refresh_store.go:114` | backlog2 §5.1 B2-A-11 |
| B2-A-28 | **Redis password 可选 fail-open** — 现状: 缺 password 仍允许连接；修复: real mode 强制 password fail-fast | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `adapters/redis/client.go:62-68` | backlog2 §5.3 B2-A-28 |
| B2-A-31 | **Redis sentinel TLS 未透传** — 现状: sentinel 模式 TLS config 未传给底层 client；修复: 透传 TLS | bug | P2/Cx2 | 🟡 | sentinel + TLS 部署 | `adapters/redis/client.go:200-215` | backlog2 §5.3 B2-A-31 |
| B2-C-12 | **Audit HMAC key 最小长度未验证** — 现状: 任意短密钥都接受；修复: 加 32 字节最小长度 + Validate | bug | P2/Cx1 | 🟡 | 发布前安全收口 | `cells/auditcore/cell.go:319` | backlog2 §4 B2-C-12 |

---

## cap-11: 分布式锁

> 主要包：`runtime/distlock` + `adapters/redis`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| DISTLOCK-RENEW-CALLER-CONTEXT-01 | **DISTLOCK-RENEW-CALLER-CONTEXT-01** — 现状: manager renewal 用 `context.Background()`，父 ctx cancel 不停续租；修复: 用 acquisition ctx 派生 renew deadline | bug | P1/Cx2 | 🟠 | 首个 prod distlock caller | `runtime/distlock/locker.go` + `manager.go` | GitHub #20 |
| DISTLOCK-WORKER | **Distlock worker 生命周期** — 现状: 缺 worker 角色；修复: 接入 worker pattern | arch-opt | Cx2 | 🟡 | — | `runtime/distlock/` | PR-A20 |
| B2-A-29 | **Redis distlock race test 缺** — 现状: distlock_test 缺并发竞争测；修复: 加 race + count=20 stress | test | P1/Cx3 | 🟡 | — | `adapters/redis/distlock_test.go` | backlog2 §5.3 B2-A-29 |
| B2-A-30 | **Redis distlock renew TTL 精度损失** — 现状: renew 时 TTL 精度损失；修复: 用 PEXPIRE 毫秒精度 | bug | P2/Cx2 | 🟡 | — | `adapters/redis/distlock.go:50-56` | backlog2 §5.3 B2-A-30 |

---

## cap-12: 启停编排 (Bootstrap)

> 主要包：`runtime/bootstrap` + `runtime/shutdown`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| V-A8-DEFERRED | **CMD-CORE-INTERNAL-GUARD-PUBLIC-01** — 现状: cmd/corebundle/main.go 28 行，archtest 锁 ≤30；修复: 触发后评估提升为公开类型 | debt | Cx2 | 🟠 | runtime/bootstrap 子包出现 / 多消费方 | `runtime/bootstrap/` + `cmd/corebundle/` | PR-A64a deferred |
| PR252-F1 | **QueueRegistrar bootstrap 集成** — 现状: 当前仅 InMemQueue；修复: 下一个 durable command adapter 落地时加入 | arch-opt | Cx3 | 🟠 | 下一个 durable command adapter | `runtime/command/` | PR#252 |
| PR252-F2 | **Sweeper 生产治理** — 现状: 单 replica 假设；修复: multi-replica command consumer 时落 | arch-opt | Cx4 | 🟠 | multi-replica command consumer | `runtime/command/` | PR#252 |
| PR333-BOOTSTRAP-OPTION-CROSS-CONCERN | **Bootstrap option 跨 concern 拆分** — 现状: option 概念混杂；修复: 按 concern 拆 | arch-opt | Cx2 | 🟡 | — | `runtime/bootstrap/` | PR#333 |
| PR405-BOOTSTRAP-SHUTDOWN-BUDGET-DECOUPLE | **BOOTSTRAP-SHUTDOWN-BUDGET-PER-LISTENER-DECOUPLE-01** — 现状: phase10 共享 shutCtx，dual-listener race 偶发超时；修复: HTTP drain + LIFO teardown 拆双 budget + 新 ADR | arch-opt | P2/Cx2 | 🟡 | — | `runtime/bootstrap/phases_shutdown.go` + `bootstrap_http_shutdown.go` + ADR | PR#405 reviewer |
| STARTUP-ROLLBACK-ERR-JOIN-01 | **Startup rollback 错误聚合** — 现状: startup 失败时 rollbackErr 静默丢；修复: `errors.Join(startupErr, rollbackErr)` 或 `StartupRollbackError{Startup, Rollback}` 结构化 | bug | P1/Cx2 | 🟡 | v1.0 GA 前 | `runtime/bootstrap/run_state.go` | backlog1 §2.4 |
| COREBUNDLE-MAINTEST-FAIL-FAST-01 | **corebundle main_test fail-fast** — 现状: bind 错误被白名单吞掉；修复: 用 `net.Listen("tcp", "127.0.0.1:0")` 注入 + 断言关键装配里程碑 | test | Cx2 | 🟡 | — | `cmd/corebundle/main_test.go` | backlog1 §2.7 |
| B2-R-01 | **HealthListener 缺失时静默回退** — 现状: bootstrap 找不到 HealthListener 时静默回退到 main listener；修复: fail-fast 或显式 opt-in fallback | bug | P2/Cx2 | 🟡 | — | `runtime/bootstrap/bootstrap_phases.go:583-596` | backlog2 §3 B2-R-01 |
| B2-R-02 | **Readyz 缺少 repo probe** — 现状: configcore/auditcore HealthCheckers 仅接 outbox，repo 状态无 probe（与 cap-13 REPO-HEALTHCHECKER-01 协同）| bug | P1/Cx2 | 🟡 | 与 cap-13 REPO-HEALTHCHECKER-01 同 PR | `cells/configcore/cell.go:204` + `cells/auditcore/cell.go:191` | backlog2 §3 B2-R-02 |
| B2-X-03 | **PG invalid index warn continue** — 现状: PG invalid index 仅 warn 继续启动；修复: 改 fail-fast 防隐藏数据完整性问题 | bug | P2/Cx2 | 🟡 | — | `cmd/corebundle/bundle.go:308-313` | backlog2 §7 B2-X-03 |
| B2-W-05 | **WebSocket Stop 同步 close 超时** — 现状: Stop 同步逐个 close 超时硬编码；修复: 并发 close + closeWg + 可配置 timeout | bug | P1/Cx2 | 🟡 | — | `runtime/websocket/hub.go:280-293` | backlog2 §6 B2-W-05 |

---

## cap-13: 可观测性

> 主要包：`runtime/observability/{metrics,tracing,poolstats}` + `pkg/logutil` + `adapters/{prometheus,otel}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| WS-HUB-READYZ-PROBE-01 | **WEBSOCKET-HUB-READYZ-PROBE-01** — 现状: Hub 不实现 ManagedResource，未就绪时 /readyz 仍 200；修复: Checkers 暴露 `websocket_hub_ready` | feat | Cx3 | 🟢 | — | `runtime/websocket/hub.go` + `health.go` (新) + bootstrap | PR-A64a #340 review #4 / 029 D7 |
| ADAPTER-MANAGED-RESOURCE-COMPLETENESS-01 | **Adapter readyz probes 完整性** — 现状: 部分 adapter 缺 ready probe；修复: 统一规范 | arch-opt | Cx2 | 🟡 | — | `adapters/{postgres,redis,s3}/` | systems layer review |
| R3 | **safe_observe DI** — 现状: observe DI 路径未统一；修复: 抽象统一 | arch-opt | — | 🟡 | — | `runtime/observability/` | R3 |
| A5a-R3 | **Observability ctx 透传** — 现状: 部分路径丢 ctx；修复: thread ctx | arch-opt | — | 🟡 | — | `runtime/observability/` | A5a |
| A5a-R12 | **Observability 集成补全** — 现状: integration test gap；修复: 加测 | test | — | 🟡 | — | `runtime/observability/` | A5a |
| PR238-FU2 | **PR238 typed gate governance** — 现状: typed gate 后续治理 (→ #321 typed gate)；修复: 跟进 | arch-opt | P2/Cx2 | 🟢 | — | `runtime/observability/` | PR#238 |
| OBS-SSA-ANALYZER-01 | **OBS SSA analyzer** — 现状: 缺静态分析；修复: 加 SSA-based analyzer | arch-opt | Cx3 | 🟡 | — | `tools/archtest/` + `runtime/observability/` | OBS-SSA |
| PR-CI-5-FU-HEALTH-LATE-WATCHER | **Health late watcher** — 现状: late watcher 路径未覆盖；修复: 补 | arch-opt | Cx2 | 🟡 | — | `runtime/http/health/` | PR-CI-5 |
| PR392-FU-AUDIT-CHAIN-WIRING | **BOOTSTRAP-AUDIT-CHAIN-WIRING-01** — 现状: onAuthFail 用 slog 未接 audit chain；修复: 升级为 audit.AppendBootstrapAuthFail | arch-opt | P2/Cx2 | 🟠 | accesscore audit chain cross-cell wiring | `cmd/corebundle/access_module.go` | PR #392 ADR §D10 |
| PR237-OB2 | **Listener observability** — 现状: per-listener 观测 metric 不全；修复: 补 | arch-opt | Cx2 | 🟡 | — | `runtime/observability/` | PR#237 |
| PR284-FU-COMPOSE-HEALTH | **Compose health** — 现状: docker-compose health 不全；修复: 补 healthcheck | arch-opt | Cx2 | 🟡 | — | `examples/*/docker-compose.yml` | PR#284 |
| PR283-OTEL-SLOG-ERROR-ATTR | **OTEL-SLOG-ERROR-ATTR-NORMALISE-01** — 现状: `slog.Any("error", err)` 在 OTEL bridge 会展开 struct；修复: ReplaceAttr hook 序列化 err.Error() | arch-opt | P2/Cx2 | 🟠 | 首次 OTEL slog bridge 接入 | `adapters/otel/` + `runtime/observability/logging/` | PR#283 round-2 I3 |
| M1-OBSERVED | **HEALTHZ-INTERFACE-PACKAGE-01** — 现状: 38 处 Health 实现分散无统一接口；修复: 新建 `kernel/healthz` 接口包 (Aggregator/Probe/Snapshot) + codegen 从 cell.yaml 派生状态 schema + 默认 `runtime/observability/healthz/inmemory` 实现 + 可选 postgres/otel adapter + `HEALTHZ-WRITE-01` archtest + 38 处分散 Health 收口（不持久化 yaml，持久化交宿主） (also: cap-14, cap-10) | feat | P2/Cx3 | 🟡 | — | `kernel/healthz/` (新) + `runtime/observability/healthz/` + `tools/codegen/` | ADR-202605041430 M1 |
| P4-TD-10 | **Metrics path label cardinality** — 现状: `r.URL.Path` 直接作 label，参数化路由展开成高基数序列（`/users/123` `/orders/42`...）；修复: 改用 chi route template 或 `_` 占位 | bug | P2/Cx2 | 🟡 | — | `runtime/observability/metrics.go` | tech-debt-registry P4-TD-10 |
| WS-T-01 | **WS Stop + external cancel 并发测试** — 现状: shutdown CAS 单路径设计未被测试锁；修复: 加并发竞争测 | test | Cx2 | 🟡 | — | `runtime/websocket/hub_test.go` | tech-debt-registry WS-T-01 |
| WS-T-02 | **Broadcast/Send on stopped hub 测试** — 现状: 停止后调用不 panic 但行为未验证；修复: 加 stopped hub 调用回归 | test | Cx1 | 🟡 | — | `runtime/websocket/hub_test.go` | tech-debt-registry WS-T-02 |
| WS-OPS-01 | **WS shutdownTimeout 可配置** — 现状: 硬编码 10s；修复: 暴露到 HubConfig | feat | Cx1 | 🟡 | — | `runtime/websocket/hub.go` | tech-debt-registry WS-OPS-01 |
| WS-OPS-02 | **WS shutdown 并发 Close** — 现状: 同步逐个 Close，千连接线性增长；修复: 并发 Close + closeWg | arch-opt | Cx2 | 🟡 | 千级连接规模出现 | `runtime/websocket/hub.go` | tech-debt-registry WS-OPS-02 |
| WS-DX-01 | **WS per-conn context tracing** — 现状: per-conn ctx 基于 Background()，无 tracing/correlation 传到 MessageHandler；修复: 透传 tracing ctx | arch-opt | Cx2 | 🟡 | observability 接入时 | `runtime/websocket/` | tech-debt-registry WS-DX-01 |
| WS-DX-02 | **WS Conn 接口缺 RemoteAddr()** — 现状: 诊断日志只有 opaque UUID；修复: 接口加 RemoteAddr() | arch-opt | Cx1 | 🟡 | — | `runtime/websocket/` | tech-debt-registry WS-DX-02 |
| B2-C-01 | **Audit hashchain 重启未恢复尾节点** — 现状: NewHashChain 启动从空链开始，多实例或重启后尾哈希不连续；修复: cell 启动时从 repo `SELECT last hash` 注入；考虑 leader 单写或 advisory lock | arch-opt | P0/Cx4 | 🔴 | — | `cells/auditcore/internal/domain/hashchain.go:31` + `cells/auditcore/cell.go` | backlog2 §1 B2-C-01 |
| B2-R-05 | **OTel metric provider ctx 固定 Background** — 现状: provider 用 ctx.Background()；修复: 透传 caller ctx | bug | P1/Cx4 | 🟡 | — | `adapters/otel/metric_provider.go:174,178,185` | backlog2 §3 B2-R-05 |
| B2-R-06 | **OTel tracer provider 未注册全局** — 现状: tracer 实例化后未 SetGlobal；修复: SetTracerProvider | bug | P1/Cx2 | 🟡 | — | `adapters/otel/tracer.go:56,73` | backlog2 §3 B2-R-06 |
| B2-R-07 | **OTel tracer shutdown 无 deadline** — 现状: shutdown 无超时上限；修复: 加 ctx deadline | bug | P1/Cx1 | 🟡 | — | `adapters/otel/tracer.go:63,65` | backlog2 §3 B2-R-07 |
| B2-R-08 | **OTel callback 需手工 unregister** — 现状: callback 注册后无自动 unregister；修复: 接 lifecycle hook | bug | P1/Cx3 | 🟡 | — | `adapters/otel/pool_collector.go:43,110` | backlog2 §3 B2-R-08 |
| B2-R-09 | **OTel attr cache key 碰撞无上界** — 现状: attr cache 无 LRU/eviction；修复: 加 LRU + max size | bug | P1/Cx3 | 🟡 | — | `adapters/otel/metric_provider.go:84,96,101` | backlog2 §3 B2-R-09 |
| B2-C-05 | **Auditappend actor 缺失降级不安全** — 现状: actor 缺失时静默降级；修复: fail-closed | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `cells/auditcore/slices/auditappend/service.go:133` | backlog2 §4 B2-C-05 |
| B2-C-09 | **Auditquery raw payload 直接回传** — 现状: handler 直接回传 raw payload 含敏感字段；修复: redact + slog level 区分 | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `cells/auditcore/slices/auditquery/handler.go:35,42` | backlog2 §4 B2-C-09 |
| B2-C-14 | **Hash-chain 跨重启连续性测试缺** — 现状: 缺重启场景验证；修复: 加 testcontainer 重启回归 | test | P2/Cx2 | 🟡 | — | `cells/auditcore/slices/auditappend/service_test.go:110` | backlog2 §4 B2-C-14 |
| B2-A-19 | **OTel span SetAttributes 明文出站** — 现状: span attr 未 redact；修复: 走 `pkg/redaction` | bug | P1/Cx2 | 🟡 | — | `adapters/otel/span.go:43,51` | backlog2 §5.3 B2-A-19 |
| B2-A-20 | **OTel simple tracer propagation 不对称** — 现状: 解析 vs 注入实现不对称；修复: 统一 propagator | bug | P2/Cx2 | 🟡 | — | `runtime/observability/tracing/tracer.go:77` | backlog2 §5.3 B2-A-20 |
| B2-A-22 | **Prometheus handler 无 timeout** — 现状: scrape 无超时控制；修复: 加 server.WriteTimeout | bug | P1/Cx1 | 🟡 | — | `cmd/corebundle/metrics.go:83` | backlog2 §5.3 B2-A-22 |
| B2-A-23 | **Prometheus cellID label 无验证** — 现状: cellID label 接受任意字符串；修复: 加 enum/格式校验 | bug | P1/Cx1 | 🟡 | — | `adapters/prometheus/hook_observer.go:114-117` | backlog2 §5.3 B2-A-23 |
| B2-A-24 | **Prometheus race test 缺** — 现状: provider 缺并发竞争测试；修复: 加 race | test | P1/Cx2 | 🟡 | — | `adapters/prometheus/metric_provider_test.go` | backlog2 §5.3 B2-A-24 |
| B2-W-03 | **WebSocket 可观测性缺** — 现状: hub 无 metric/log；修复: 加 connection count / message rate / shutdown duration metric | feat | P1/Cx2 | 🟡 | — | `runtime/websocket/hub.go` | backlog2 §6 B2-W-03 |
| REPO-HEALTHCHECKER-01 | **configcore/auditcore repo 接 HealthCheckers** — 现状: HealthCheckers 仅接 outbox，关键 repo 未接探针；修复: 接入 cell HealthCheckers（与 PR-CFG-1 PG relay probe 同主题）| arch-opt | P1/Cx2 | 🟡 | 与 PR-A53 同 PR | `cells/configcore/cell.go` + `cells/auditcore/cell.go` | backlog1 §3 |

---

## cap-14: 代码生成与治理工具链

> 主要包：`tools/{archtest,codegen,depgraph,e2egate,metricschema,generatedverify}` + `cmd/gocell` 8 子命令

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| K05-CLI-FLAG-DEFAULT-AND-SCAFFOLD | **K05-CLI-FLAG-DEFAULT-AND-SCAFFOLD** — 现状: codegen 子命令 flag 默认值 + scaffold 模板未做产品评审建议；修复: --all/--local 默认 true + scaffold 自带 stub markers + Levenshtein 建议 | feat | Cx1 | 🟢 | — | `cmd/gocell/app/codegen_cmd.go` + `scaffold_cmd.go` + 模板 | K#05 ADR Decision 8 |
| K05-ARCHTEST-PACKAGES-LOAD-UPGRADE | **K05-ARCHTEST-PACKAGES-LOAD-UPGRADE** — 现状: archtest AST 仅按 `reg` 字面 receiver 匹配，rename 可绕过；修复: 升 packages.Load + 按 cell.Registry 类型判断 | arch-opt | Cx3 | 🟠 | K#06 contractgen 类型分析 | `tools/archtest/codegen_unified_test.go` | K#05 PR #365 review K05-07 |
| TEST-JOURNEY-ROOT-HARNESS-01 | **ROOT-JOURNEY-INTEGRATION-HARNESS-01** — 现状: J-useronboarding 等 root journey 缺真 Go integration harness；修复: 补 tests/integration/ | test | Cx3 | 🔴 | — | `tests/integration/` + `journeys/J-*.yaml` | PR-A63 复核 |
| V-A11 | **GOVERNANCE-EXAMPLES-COVERAGE-01** — 现状: governance rules 不扫 examples/；修复: 加 rules_examples.go | arch-opt | Cx3 | 🔴 | — | `kernel/governance/rules_examples.go` (新) | verification §A11 |
| V-A13 | **GENTPL-LIFECYCLE-PATTERN-01** — 现状: gentpl/main.go.tpl 直连 app.Start/Stop 跳过 phase3b（PR#392 已删 phase3b admin provision）；修复: 决定模板"最小骨架 vs 开箱即用" + 集成测试 | doc+arch-opt | Cx1+Cx2 | 🟡 | — | `kernel/assembly/gentpl/main.go.tpl` | PR #243 review §E1 |
| PR245-F6 | **OUTBOX-ARCHTEST-SCAN-SCOPE-EXPAND-01** — 现状: isCellFile 仅匹配 `cell.go`；修复: 改为 `cells/<n>/*.go` 排除 internal/slices/test | arch-opt | Cx2 | 🟡 | — | `tools/archtest/outbox_cell_test.go` | PR#245 round-1 F-6 |
| PR245-F10 | **CELL-RAW-DEPS-ARCHTEST-EXPAND-01** — 现状: PR-A5c 仅 ban WithPublisher/WithOutboxWriter；修复: 一并 ban 所有 raw-dep Option（029 #13 PR-A22 / 030 G-17 吸收） | arch-opt | Cx2 | 🟢 | — | `tools/archtest/raw_deps_test.go` (或扩展) | PR#245 round-1 F-10 |
| PR250-F3 | **Event wire byte pinning** — 现状: 缺 byte 级回归；修复: 加 pinning test | test | Cx2 | 🟡 | — | `cells/accesscore/` | PR#250 |
| PR-CFG-A-DEFER-3 | **Health agg archtest** — 现状: aggregator archtest 待 G1/G2 后落地；修复: 写 archtest | arch-opt | Cx3 | 🟢 | — | `tools/archtest/` | PR#268 |
| JOURNEY-ACTIVE-LIFECYCLE-EMPTY-01 | **Journey active lifecycle 空状态** — 现状: 无 active journey 时 governance 沉默；修复: 加守卫 | arch-opt | Cx2 | 🔴 | — | `journeys/` + governance | systems layer review |
| JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 | **Journey contract 存在性校验** — 现状: journey 引用 contract 不存在不报错；修复: 加 governance 规则 | arch-opt | Cx2 | 🔴 | — | `kernel/governance/` | systems layer review |
| ASSEMBLY-SCAFFOLD-CMD-01 | **ASSEMBLY-SCAFFOLD-CMD-01** — 现状: scaffold 子命令无 assembly；修复: 加 `gocell scaffold assembly` + 派生 modules_gen.go | feat | P1/Cx2 | 🟠 | 加第 2 个 assembly | `cmd/gocell/app/scaffold_assembly.go` (新) | systems-layer-07 §P1-3 |
| B2-K-08-CARVEOUT-NARROW | **B2-K-08-CARVEOUT-NARROW** — 现状: errcode_constructor_test 对 ctxcancel/httputil 做 file-level 豁免；修复: 改 function-level + 扩展 message const | arch-opt | P1/Cx2 | 🟡 | 第 3 个 file 豁免出现 | `tools/archtest/` + `pkg/ctxcancel/` + `pkg/httputil/` | PR#391 K#08 carve-out |
| JOURNEY-STATUS-BOARD-LIFECYCLE-CONSISTENCY-01 | **Journey status-board 状态机一致性** — 现状: board state 与 yaml lifecycle 双状态机各表；修复: 定义强映射 + 双向校验 | arch-opt | P1/Cx2 | 🟡 | 第 9 条 journey 写 board 时 | `kernel/governance/rules_journey.go` + status-board + J-*.yaml | systems-layer-06 §P1-4+5 |
| IDUTIL-UUID-RAND-FAILURE-TEST-01 | **UUID rand failure test** — 现状: rand.Read 失败路径无回归；修复: fault injection test | test | Cx1 | 🟡 | — | `pkg/idutil/` | GitHub #23 |
| FU2-GOVERNANCE-STATIC | **Governance static analysis** — 现状: typed gate (→ PR#321) 已落，static 后续；修复: 跟进 | arch-opt | Cx3 | 🟢 | — | `tools/archtest/` | — |
| PR266-AUDITAPPEND-STRICT | **AuditAppend strict** — 现状: append 验证缺；修复: 加严 | arch-opt | P2/Cx2 | 🟡 | — | `cells/auditcore/` | PR#266 |
| PR266-METADATA-ONLY-CONSUMER-BUSINESS | **Metadata-only consumer business** — 现状: receive placeholder 仍业务化；修复: 与 ConfigReceive cleanup 一同 | arch-opt | P2/Cx2 | 🟠 | 与 ConfigReceive cleanup 同 | `cells/accesscore/` | PR#266 |
| PR332-VERIFY-GENERATED-REMEDIATION-DRIFT-01 | **Verify codegen drift remediation 提示** — 现状: drift 报错不提示修复命令；修复: 补 hint | arch-opt | Cx2 | 🟡 | — | `cmd/gocell/` | PR#332 |
| PR-CI-5-FU-PANIC-WHITELIST-STRUCTURED | **PANIC whitelist structured** — 现状: 白名单字符串散；修复: structured registry | arch-opt | Cx3 | 🟡 | — | `tools/archtest/` | PR-CI-5 |
| VERIFY-CODEGEN-SANDBOX-INTEGRATION | **VERIFY-CODEGEN-SANDBOX-INTEGRATION** — 现状: --local=false sandbox 路径无端到端回归；修复: 补 1-2 条 git worktree integration test | test | Cx2 | 🟠 | 修改 verify-codegen-*.sh 或 runVerifyCodegen* | `cmd/gocell/app/codegen_*_drift_test.go` + tools/codegen helper | PR #404 K#10 review P2 |
| PR391-CLI-EXPORT-ALIAS-GENERATEDAT-FLAKE | **CLI export alias/generatedAt flake** — 现状: 测试偶发；修复: 决定确定性策略 | bug | Cx1 | 🟡 | — | `cmd/gocell/` | PR#391 |
| F2 | **Framework doc F2** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F2 |
| F3 | **Framework doc F3** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F3 |
| F4 | **Framework doc F4** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F4 |
| F5 | **Framework doc F5** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F5 |
| F6 | **Framework doc F6** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F6 |
| F7 | **Framework doc F7** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F7 |
| F8 | **Framework doc F8** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F8 |
| F9 | **Framework doc F9** — 详情待确认 | doc | Cx2 | 🟡 | — | `docs/` | F9 |
| NOLINT-AUDIT-01 | **Nolint audit** — 现状: 全仓 101 处 nolint 含 errcheck 类豁免；修复: 审查 | arch-opt | Cx2 | 🟡 | — | 全仓 *.go | NOLINT-AUDIT-01 |
| ADR-INDEX-01 | **ADR index** — 现状: 缺 ADR 索引；修复: 生成 docs/architecture/INDEX.md | doc | Cx1 | 🟡 | — | `docs/architecture/` | ADR-INDEX-01 |
| TEST-CHDIR-PARALLEL-CLI-01 | **TEST-CHDIR-PARALLEL-CLI-01** — 现状: 4 个 CLI test 用 os.Chdir 阻碍 t.Parallel()；修复: 抽 RootResolver helper | test | P3/Cx2 | 🟡 | CLI 测试 > 30s 或新 generate sub-cmd | `cmd/gocell/app/generate_*_test.go` + `verify_codegen_*_test.go` | PR #361 round-2 #3 |
| T6 | **CONTRACT-EVENT-PAYLOAD-CODEGEN-01** — 现状: scaffold/generate 无 schema → Go 能力；修复: 派生 payload.gen.go + decode/validate helper | feat | — | 🟠 | event subscriber decode 扩散 ≥5 cell | `tools/codegen/eventgen/` (新) + `generated/contracts/event/` | T6 |
| T7 | **CH-05 alias eval** — 现状: import alias / const eval 漂移；修复: governance 加 | arch-opt | — | 🟡 | import alias / const drift | `kernel/governance/` | T7 / PR-A45 |
| T9 | **Internal clients declared** — 现状: contract reality gap；修复: governance 强制 internal client 声明 | arch-opt | — | 🟠 | contract reality gap | `kernel/governance/` | T9 / PR#293 |
| T10 | **Devtools cell promotion** — 现状: catalog 内置；修复: 升级为外部 cell | arch-opt | — | 🟠 | catalog customization | `cells/devtools/` + `runtime/` | T10 / PR-A37 |
| M4-COVERAGE | **REVERSE-COVERAGE-ARCHTESTS-01** — 现状: 缺 5 条反向追溯规则；修复: 加 `IMPL-DECL-COVER-01` (cell 间 Go import 必须经 contract，非 slice 间) + `HANDLER-DECL-COVER-01` (http handler 必须出现在某 contract.yaml) + `EMIT-DECL-COVER-01` (outbox emit 必须出现在 contract.triggers) + `DEAD-CONTRACT-01` (active contract 必须有 handler 入口) + `DEAD-CODE-01` (deprecated contract 引用代码不能在 main 分支)；不含 SLICE-DECOUPLE | arch-opt | P2/Cx3 | 🟠 | M3 落地 | `tools/archtest/` | ADR-202605041430 M4 |
| CONTRACT-BREAKING-01 | **`gocell check contract-breaking`** — 现状: 缺 API schema 历史破坏性变更比对；修复: 借鉴 buf.build 引入 40+ 条规则（字段删除/必填放宽阻断） | feat | P2/Cx3 | 🟡 | V1.1 启动 | `cmd/gocell/` + `kernel/governance/` | backlog_later §5 |
| CONTRACT-CODEGEN-01 | **Go DTO ↔ JSON Schema 双向推断** — 现状: 代码与契约 YAML 分裂；修复: Struct Tags 实时双写到 JSON Schema（对齐 oapi-codegen） | feat | P2/Cx3 | 🟡 | V1.1 启动 | `tools/codegen/` + DTO 模板 | backlog_later §5 |
| CONTRACT-STUB-01 | **Consumer-Driven Contract Stub** — 现状: 缺消费方 stub 校验；修复: 提供 Stub 桩代码套件（对标 Spring Cloud Contract / Pact） | feat | P2/Cx3 | 🟡 | V1.1 启动 | `tools/contracttest/` | backlog_later §5 |
| C-L6 | **Contract ID 解析标准统一** — 现状: CLI 用点分（http.auth）、Generator 退化为斜杠分割，开发者上下文脱节；修复: 全局检索 + 统一内部 Contract ID 解析 | bug | P2/Cx2 | 🟡 | — | `cmd/gocell/` + `kernel/scaffold/` + `tools/codegen/` | backlog_later §6 C-L6 |
| CONTRACTTEST-SCHEMAREF-FAILFAST-01 | **contracttest schemaRefs 默认 fail-fast** — 现状: 未命中 schemaRefs key 默认 no-op，掩盖测试缺失；修复: 默认 fail；宽松改显式 `WithMissingKeyTolerated()` API | arch-opt | P1/Cx2 | 🟡 | 发布前必做 | `pkg/contracttest/contracttest.go` | backlog1 §2.2 |
| CONTRACT-ENDPOINT-TEST-MAPPING-01 | **active contract → 测试用例映射门禁** — 现状: 缺活跃端点 → 测试覆盖映射；修复: governance 加规则：`lifecycle: active` HTTP contract 必须有对应 contract test | arch-opt | P1/Cx2 | 🟡 | 发布前必做 | `kernel/governance/` | backlog1 §2.2 |
| CONTRACT-PATH-QUERY-EXECUTABLE-01 | **path/query 参数约束可执行测试** — 现状: pattern/min/max/format 无入参可执行测试；修复: 加 transport 入参 rejected 用例覆盖 | arch-opt | P1/Cx2 | 🟡 | 发布前必做 | `pkg/contracttest/contracttest.go` | backlog1 §2.2 |
| CLI-SECONDARY-HELP-01 | **CLI 二级命令统一 -h/help** — 现状: 二级命令不识别 `-h`/`help`，被当 subtype 解析；修复: 修正 dispatch 顺序 + 文案 | bug | Cx1 | 🟡 | — | `cmd/gocell/app/dispatch.go` + `check.go` + `scaffold.go` | backlog1 §2.7 |
| CLI-UNIMPL-HIDE-01 | **CLI 未实现命令隐藏** — 现状: `not implemented` 命令出现在主帮助；修复: 移除或显式 `[experimental]` 标注 + 运行时 `exit 64` | bug | Cx1 | 🟡 | — | `cmd/gocell/app/dispatch.go` + `generate.go` | backlog1 §2.7 |
| B2-K-08 | **Assembly race test 认知复杂度超限** — 现状: `TestAssembly_StartConcurrentSnapshots_RaceDetector` SonarCloud `brain-overload` 32/15；修复: 拆 setupRaceFixture/spawnReaders/awaitReady 三 helper（保持 race window 确定性） | refactor | P2/Cx2 | 🟡 | — | `kernel/assembly/snapshots_race_test.go:36-120` | backlog2 §2 B2-K-08 |
| B2-A-13 | **PG pool tx rollback 日志泄漏** — 现状: rollback 日志输出 SQL 片段；修复: 走 `pkg/redaction` | bug | P2/Cx2 | 🟡 | — | `adapters/postgres/pool.go:87,113` | backlog2 §5.1 B2-A-13 |
| B2-A-21 | **OTel messaging collector format %** — 现状: format 字符串遗留 `%`；修复: 修 format 占位符 | bug | P2/Cx1 | 🟡 | — | `adapters/otel/messaging_channel_collector.go:65` | backlog2 §5.3 B2-A-21 |
| B2-A-25 | **Prometheus lookup vec 99% 重复** — 现状: 多处 lookup vec 模板代码 ~99% 重复；修复: 抽 helper 收敛 | refactor | P2/Cx2 | 🟡 | — | `adapters/prometheus/metric_provider.go:201-227` | backlog2 §5.3 B2-A-25 |
| B2-A-34 | **Redis cluster CI live gate 缺** — 现状: integration_cluster build tag 已加但 CI 未启用 live job；修复: 加 GH Actions cluster job | test | P2/Cx3 | 🟡 | — | `.github/workflows/_build-lint.yml` + `adapters/redis/cluster_real_test.go` | backlog2 §5.3 B2-A-34 |
| B2-X-01 | **Outbox E2E 固定 sleep** — 现状: integration test 含固定 `time.Sleep`；修复: 改 condition wait | test | P2/Cx1 | 🟡 | — | `cmd/corebundle/outbox_e2e_integration_test.go:169` | backlog2 §7 B2-X-01 |
| B2-X-02 | **shared-deps 聚合过宽** — 现状: shared_deps.go 聚合范围过宽，单一 struct 含太多字段；修复: 按 concern 拆 | refactor | P2/Cx3 | 🟡 | — | `cmd/corebundle/shared_deps.go:32` | backlog2 §7 B2-X-02 |
| B2-X-05 | **gocell generate indexes 未实现但可见** — 现状: 出现在 help，运行 hard fail；修复: 标 `[experimental]` 或移除 (与 cap-14 CLI-UNIMPL-HIDE-01 同主题但具体到 generate indexes) | doc | P1/Cx1 | 🟡 | — | `cmd/gocell/app/generate.go:34` | backlog2 §7 B2-X-05 |
| B2-X-06 | **gocell verify ctx 透传不完整** — 现状: verify 子命令 ctx 不一致；修复: 统一 ctx 链 | bug | P1/Cx2 | 🟠 | ctx 传播缺失暴露 | `cmd/gocell/app/verify.go:101,163,165,241` | backlog2 §7 B2-X-06 |
| B2-X-07 | **gocell dispatch 无 signal ctx** — 现状: 主入口不处理 SIGINT/SIGTERM；修复: 加 signal.NotifyContext | bug | P1/Cx2 | 🟠 | signal 不响应暴露 | `cmd/gocell/app/dispatch.go:20` + `cmd/gocell/main.go:13` | backlog2 §7 B2-X-07 |
| B2-X-08 | **cmdrun Windows 进程组杀不完** — 现状: Windows 平台进程组不彻底；修复: JobObject 或 taskkill /T | bug | P2/Cx2 | 🟡 | Windows 平台用例 | `pkg/cmdrun/cmdrun_windows.go` | backlog2 §7 B2-X-08 |
| P2-T-02 | **J-auditlogintrail 端到端集成测试** — 现状: stub 已就位；修复: 用 Docker + testcontainers 激活 | test | P2/Cx2 | 🟡 | Phase 5 启动 | `tests/integration/` + journey | tech-debt-registry P2-T-02 |

---

## cap-x-cross: 横切

> 不属于单一 capability 的项：CI / lint baseline、跨 capability 大重构（≥ 4 cap 且无明确 owner）、仓库级文档、发布相关 checklist。

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR-BATCH2-RETRO-FU | **Batch2 retrospective 收口** — 现状: 多个跨 cap 发现；修复: 拆条 fix-up | arch-opt | Cx1-Cx2 | 🔴 | — | `runtime/auth/` + `cells/` | batch2 retrospective |
| ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 | **A-03 ADAPTER-ERROR-CLASSIFICATION-TRANSIENT** — 现状: 各 adapter 错误分类不统一；修复: postgres 40001/40P01/08* + redis i/o timeout + s3 5xx/429 标 transient | arch-opt | Cx3 | 🟠 | 首个 handler disposition 收口 | `adapters/{postgres,redis,s3}/errors.go` + `pkg/errcode/` | systems layer review |
| ADAPTER-FAKE-EXPORT-01 | **Adapter fake export 一致性** — 现状: fake/exports 散；修复: 统一规范 | arch-opt | Cx3 | 🟠 | cell mock 扩展 | `adapters/*/fake/` | systems layer review |
| PR-A41-FU1 | **PR-A41 advisory rules follow-up** — 现状: governance advisory 规则余项；修复: 跟进 | arch-opt | Cx2 | 🟡 | — | `kernel/governance/` | PR-A41 |
| PR237-F06 | **Listener DX follow-up** — 现状: Listener DX 余项；修复: 跟进 | arch-opt | Cx2 | 🟡 | — | `runtime/http/middleware/` | PR#237 |
| PR237-DX1 | **Listener DX docs** — 现状: DX docs 缺；修复: 补 | doc | Cx2 | 🟡 | — | `docs/` | PR#237 |
| PR237-A4 | **Listener architecture** — 现状: 双 listener 架构 doc 缺；修复: 写架构说明 | arch-opt | Cx2 | 🟡 | — | `runtime/http/` | PR#237 |
| PR238-FU4 | **PR238 audit follow-up 4** — 详情见 PR#238 | arch-opt | Cx2 | 🟡 | — | `cells/auditcore/` | PR#238 |
| PR238-FU5 | **PR238 audit follow-up 5** — 详情见 PR#238 | arch-opt | Cx2 | 🟡 | — | `cells/auditcore/` | PR#238 |
| PR238-FU8 | **PR238 audit follow-up 8** — InternalMessage op label 测试覆盖；修复: configrepo UpdateForRollback op 测试 | test | P2/Cx1 | 🟡 | — | `cells/configcore/internal/adapters/postgres/config_repo_test.go` | PR#238 |
| PR280-FU1 | **PR280 adapter follow-up 1** — 详情见 PR#280 | arch-opt | Cx2 | 🟡 | — | `adapters/` | PR#280 |
| DEVOPS-INTEGRATION-CLEANUP-WAIT-TIMEOUT-01 | **Devops integration cleanup wait timeout** — 现状: e2e cleanup 超时；修复: 加 wait helper | arch-opt | Cx1 | 🟡 | — | `tests/e2e/` | GitHub #19 |
| DEAD-VARIABLE-01 | **DEAD-VARIABLE-DEADCODE-SCAN-01** — 现状: golangci-lint 未启 unused/deadcode；修复: 临时启用 → baseline → 删 / //nolint:unused → 关闭 | test | P3/Cx1 | 🟡 | — | `.golangci.yml`（临时改）+ 全仓清理 | graceful-backus P3 P2-6 |
| X4 | **WM-7 泛型 BulkResult** — 现状: 各 cell 各写 BulkResult；修复: 抽泛型 | feat | P3/— | 🟡 | — | `pkg/` | 历史 Batch 8 |
| X9 | **LINT-MODERN-01** — 现状: modernization baseline 全仓清理（rangeint / stringsseq / forvar / inline / testingcontext / any / nhooyr.io→coder）；修复: 独立 PR；不混入功能 | arch-opt | P3/Cx2 | 🟡 | — | 全仓 | PR#163 post-review |
| PR267-FU-IOTDEVICE-OWNER | **iotdevice owner.team 同步** — 现状: example owner 缺；修复: 补 | arch-opt | Cx1 | 🟡 | — | `examples/iotdevice/` | PR#267 |
| PR267-FU-TODOORDER-OWNER | **todoorder owner.team 同步** — 现状: 同上；修复: 同上 | arch-opt | Cx1 | 🟡 | — | `examples/todoorder/` | PR#267 |
| B-FLOOR-FOLLOWUP | **TYPED-ENVELOPE-ADAPTER-FLOOR-UPGRADE** — 现状: PR#403 段 1 是 Ceiling 守；修复: 段 2.5 升 Success-Floor + 段 4 升 Full-Floor | refactor | 段 2.5 Cx3 / 段 4 Cx3 | 🟠 | 段 2 invariant Registry 工具产品化 | `cells/*/slices/*/handler.go` (~20) + archtest + ADR D7 演进锚点 | PR #403 第三轮 review §R1 |
| KERNEL-WEBHOOK-01 | **kernel/webhook 出站请求** — 现状: 缺 Webhook Receiver/Dispatcher 抽象；修复: 新建 webhook 包 + HMAC 认证 + SSRF 黑白名单（依赖 Outbox Relay 稳定）(also: cap-04, cap-08) | feat | P2/Cx3 | 🟡 | Outbox Relay 稳定后 | `kernel/webhook/` (新) | backlog_later §2 + WM-4 |
| RUNTIME-SCHEDULER-01 | **runtime/scheduler Cron 调度** — 现状: PeriodicWorker 仅固定间隔；修复: 新建 scheduler 包 + Cron 表达式 + 分布式防重 (also: cap-11, cap-12) | feat | P2/Cx3 | 🟡 | 业务出现 Cron 需求 | `runtime/scheduler/` (新) | backlog_later §2 |
| KERNEL-ROLLBACK-01 | **kernel/rollback 元数据模型** — 现状: 缺跨事件撤回原语；修复: 新建 rollback 包 + 元数据模型 (also: cap-07, cap-08) | feat | P3/Cx3 | 🟡 | V1.1+ 启动 | `kernel/rollback/` (新) | backlog_later §2 |
| L3-EXAMPLE-PROJECTION-01 | **examples L3 投影 reference** — 现状: 无完全 L3 一致性级别官方 reference cell，业务可能错误塞入 L2；修复: examples/ 补 L3 Projection 样板 (also: cap-08) | doc | P2/Cx2 | 🟡 | v1.1 启动 | `examples/` | backlog_later §4 |
| C-DC9 | **auditarchive 死代码靶子打通** — 现状: handler 返 `ErrNotImplemented`，S3 adapter 已就绪但中间业务层漏接；修复: 打通导出链路 (also: cap-08) | bug | P2/Cx2 | 🟡 | — | `cells/auditcore/slices/auditarchive/` + S3 adapter | backlog_later §6 C-DC9 |
| P3-TD-04 | **websocket/oidc/s3 sandbox httptest panic** — 现状: sandbox 限 net.Listen，单测 panic；guard 已加；修复: 评估 CI sandbox 替代方案或维持 guard | test | Cx1 | 🟡 | — | `adapters/{websocket,oidc,s3}/` + CI | tech-debt-registry P3-TD-04 |
| P3-TD-05 | **示例 docker-compose start_period** — 现状: 3 个示例 compose 缺 start_period（rabbitmq healthcheck）+ 用废弃的 `version: "3.9"`；修复: 补 start_period + 删 version 键（合并 P4-TD-07） | arch-opt | Cx1 | 🟡 | v1.1 启动 | `examples/*/docker-compose.yml` | tech-debt-registry P3-TD-05 + P4-TD-07 |
| P4-TD-01 | **noop outbox/Claimer 共享包** — 现状: 各处 ad-hoc noop 实现，KG-02 建议提取；修复: 抽到共享 `runtime/testutil/outbox/` + 测试 helper 收口 | refactor | Cx2 | 🟡 | — | `runtime/testutil/` (扩) + 各 cell 测试 | tech-debt-registry P4-TD-01 |
| P4-TD-06 | **CI example validation `\|\| true` 形式化** — 现状: 验证错误被静默吞咽；修复: 删 `\|\| true` 让 CI 阻断 | bug | Cx1 | 🟡 | v1.1 启动 | `.github/workflows/` | tech-debt-registry P4-TD-06 |
| P4-TD-09 | **testcontainers-go indirect 标记** — 现状: go.mod 标记 indirect 但实际直接依赖，go mod tidy 可能移除；修复: 改 direct dep | bug | Cx1 | 🟡 | — | `go.mod` | tech-debt-registry P4-TD-09 |
| B2-C-13 | **L2 跨层 e2e 回归不足** — 现状: setup → audit → config 跨 cell e2e 不全；修复: 加跨 cell integration test | test | P2/Cx3 | 🟡 | — | `cells/accesscore/slices/setup/service_test.go` + `tests/integration/` | backlog2 §4 B2-C-13 |
| B2-T-07-FU-4 | **SVCTOKEN 跨信任域限制** — 现状: 跨 trust domain 时 SVCTOKEN 无额外限制；修复: 加 trust domain claim + 验证（A5 follow-up） | arch-opt | Cx4 | 🟠 | 多租户/跨信任域需求 | `contracts/` + `runtime/auth/` | backlog2 §8 A5 follow-up |
| ADAPTER-CONNECT-BUDGET-01 | **adapter 级 ConnectTimeout 强制** — 现状: 各 adapter 依赖上层 ctx；修复: adapter 级 ConnectTimeout（默认 5s）写 Config + Validate + `ERR_ADAPTER_CONNECT_TIMEOUT` (also: cap-08, cap-10；PG 部分由 PR#401 已部分覆盖) | bug | P1/Cx2 | 🟡 | v1.0 GA 前 | `adapters/rabbitmq/connection.go` + `adapters/postgres/pool.go` | backlog1 §2.4 |
| S3-FAILURE-INJECTION-01 | **S3 故障注入测试** — 现状: 缺 MinIO testcontainer 集成测；修复: 上传 403/5xx/timeout/recovery 路径覆盖 (also: cap-13) | test | P1/Cx2 | 🟡 | v1.0 GA 前 | `adapters/s3/s3_test.go` | backlog1 §2.5 |
| SWEEPER-OBSERVABLE-01 | **Sweeper onError + 并发度** — 现状: onError 默认兜底（slog.Error）；并发度按 finding 数计算不准；修复: onError 注入 + 并发度按 `groups × capacity × cost` 计算 (also: cap-08, cap-13；与 PR252-F2 同 PR) | arch-opt | P1/Cx2 | 🟠 | 与 PR252-F2 同 batch | `kernel/command/sweeper.go` | backlog1 §3 |

---

## 历史与参考

- 原 backlog 305 行已备份到 [`docs/backlog/archive/backlog.md`](backlog/archive/backlog.md)（develop @ 18a06ab7 快照），含被本次迁移**跳过**的 narrative 段：
  - `## 架构演进里程碑（M0-M4，源自 ADR-202605041430）` — **M0 已大部分完成**（poolstats 接口下沉 PR#387 / Noop archtest / CellMeta 合一）；**M1/M2/M3/M4 已提取为 4 条 backlog item**（M1→cap-13、M2→cap-02、M3→cap-02、M4→cap-14）；narrative 段保留在 archive 作为完整 ADR 上下文
  - `## 设计决策记录（历史 — 不修，避免重复审查）`
  - `## v1.1+ 长期规划`
  - `## 工时汇总`
- `docs/backlog1.md` / `docs/backlog2.md` / `docs/backlog_later_detail.md` / `docs/tech-debt-registry.md` 在 P2-P6 期间逐步并入本文件，最终改成 1 段重定向桩。
- 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
