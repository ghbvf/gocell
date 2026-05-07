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
| B2-PROVISIONER-MUTEX-REVIEW | **Provisioner mutex 清理 review** — 现状: A26-R1 已删 initialadmin，但 provisioner mutex 残留；修复: PG adapter 落地后审视是否仍需 mutex | refactor | P2/Cx1 | 🟠 | PG adapter for accesscore | `cells/accesscore/internal/adminprovision/provisioner.go` | backlog2 §13 |

---

## cap-02: 元数据解析与治理

> 主要包：`kernel/metadata` + `governance` + `verify` + `depgraph` + `tools/archtest` + `tools/generatedverify`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| P1-5 | **METADATA-PERF-BENCH-01** — 现状: 缺 `BenchmarkParseFS_500Files` 性能基准；修复: 加 bench + 评估 goccy/go-yaml 单次解码迁移成本 | test | P1/Cx3 | 🟡 | — | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| KERNEL-CONTRACTSPEC-CONTRACTMETA-DUAL-DEF-01 | **Contract 双源定义** — 现状: `kernel/wrapper.ContractSpec` 与 `kernel/metadata.ContractMeta` 双源；修复: K#04 PR-4 codegen 落地时合一 | arch-opt | Cx3 | 🟠 | K#04 PR-4 codegen 迁移 | `kernel/wrapper/` + `kernel/metadata/` | systems layer review |
| KERNEL-INTERNAL-DAG-GUARD-01 | **kernel 反向 import 守卫** — 现状: 缺 archtest 守 kernel 反向 import；修复: 引入新依赖时一并加 DAG 守卫 | arch-opt | Cx2 | 🟠 | kernel 出现新反向引用 | `tools/archtest/` | systems layer review |
| SHARED-ERROR-SCHEMA-GENERATION-01 | **共享 error schema 单源** — 现状: 4 份 mirror 人工同步；修复: canonical → make generate 派生 examples/testdata | arch-opt | P2/Cx2-Cx3 | 🟡 | 下次 envelope schema 变更 | `contracts/shared/errors/` + `tests/contracttest/testdata/` | PR#396 review |
| KERNEL-DEPGRAPH-OUT-EVAL-01 | **Depgraph out evaluation** — 现状: depgraph 只 in-eval；修复: 加 out-eval 路径 | arch-opt | Cx3 | 🟠 | 第 3 个 depgraph 消费方 | `kernel/depgraph/` + `runtime/` | PR#357 |
| CELLS-SLICE-MULTI-VERB-DECOMPOSE-01 | **Slice 多 verb 拆分** — 现状: auditappend 14 contractUsages、configread 双 listener；修复: 拆 `auditappend-{session,user,config,role}` 共享 dispatch + `configread-internal` 单独，不留兼容包装 | refactor | P1/Cx3 | 🟡 | — | `cells/auditcore/slices/auditappend/` + `cells/configcore/slices/configread/` | systems layer review + 030 §2 K-07 |
| M2-LIFECYCLE | **CELL-SLICE-LIFECYCLE-FIELD-01 + STATE-MACHINE-EXPLICIT** — 现状: (a) cell/slice 缺生命周期相位声明；(b) `cell.lifecycle` 与 `outbox.entry.state` 隐含状态机，无 enum + transition 表；修复: cell.yaml/slice.yaml 加 `lifecycle` 字段 (experimental/candidate/asset/maintenance/retired) + `kernel/cell/lifecycle.go` 显式 `state enum + transition map` + `kernel/outbox/state.go` 同款 + governance 校验状态转移合法性 + archtest 校验状态转移完备性 + 运行时通过 Aggregator 接口暴露当前相位（差距由消费方计算）+ 1 ADR (also: cap-13) | feat | P2/Cx3 | 🟠 | M1 落地 | `kernel/metadata/types.go` + `kernel/cell/lifecycle.go` + `kernel/outbox/state.go` + `kernel/governance/` + `kernel/healthz/` + ADR | ADR-202605041430 M2 + 030 §3 F-08 |
| M3-RULE-ENGINE | **GOVERNANCE-RULE-ENGINE-DATA-DRIVEN-01** — 现状: governance 64 规则散在 Go 代码；修复: `kernel/governance/engine.go` 唯一执行体 + `kernel/governance/rules/*.yaml` 数据化（5 槽位 detect/evidence/next/level/harvest）+ `next-action` 五级 (autofix/suggest/advisory/block/escalate) + 规则带 `metric` 距离函数 + 修 ADV-05 SeverityError 错分 | refactor | P2/Cx3 | 🟡 | — | `kernel/governance/engine.go` (新) + `kernel/governance/rules/*.yaml` (新) | ADR-202605041430 M3 |
| G-1 | **FMT-11 dynamic-status-field 隔离** — 现状: 动态状态字段（readiness/risk/blocker）漏入非 status-board 文件，元数据被污染；修复: governance 加 FMT-11 严格隔离 | doc | P2/Cx2 | 🟡 | 出现元数据污染或非法 contract 引用 | `kernel/governance/` | backlog_later §1 |
| DURABLE-TYPE-01 | **L2/L3 持久化级别静态保护** — 现状: 类型抹除让 L2/L3 检测退化为启动期 panic；修复: 探索类型系统层面静态编译保护（仓储级能力推断） | arch-opt | P2/Cx3 | 🟡 | v1.1 启动 | `kernel/metadata/` + `kernel/persistence/` | backlog_later §6 |
| B2-K-05 | **Metadata parser error 路径泄漏** — 现状: parse error 含 fs 内部路径，低强度信息泄露；修复: error 双通道 (public 仅 cell/slice ID + 字段路径，internal slog 保留 fs path) | bug | P2/Cx2 | 🟡 | — | `kernel/metadata/parser.go:190,202` | backlog2 §2 B2-K-05 |
| B2-K-07 | **Contracttest undeclared ref no-op** — 现状: `MustValidateRequest("not-declared", ...)` 静默 return，key 写错时假通过；修复: 未声明 key 改 `t.Fatalf` | bug | P1/Cx1 | 🟡 | — | `pkg/contracttest/contracttest.go:170,189` | backlog2 §2 B2-K-07 |
| B2-T-07-FU-3 | **K04-CELLGEN-CONTRACTSPEC-CLIENTS** — 现状: cellgen 不派生 contract.clients；修复: 加派生（A5 follow-up） | arch-opt | Cx2 | 🟡 | cellgen 升级窗口 | `tools/codegen/cellgen/` | backlog2 §8 A5 follow-up |
| GOVERNANCE-INVARIANTS-REGISTRY | **派生物治理 invariants Registry** — 现状: 资源约束在 6-8 处独立声明，加新字段改 6-8 处；修复: 立 `kernel/governance/invariants.go::Registry` + 四件套（detect/evidence/next/level/harvest），M3-RULE-ENGINE 子项聚焦派生 invariants | refactor | P2/Cx3 | 🟡 | 与 M3-RULE-ENGINE 同根 | `kernel/governance/invariants.go` (新) + `kernel/governance/rules/*.yaml` | PR#403/404 R0 |
| GOVERNANCE-AUTH-PUBLIC-INTERNAL-FORBIDDEN | **static governance 禁 auth.public 在 /internal/v1/** — 现状: runtime 守存在，元数据 governance 阶段无闸门；修复: 加 FMT-XX 规则 + codegen fail-fast，contract 出现 `auth.public:true` + `/internal/v1/*` 路径模式即报错 | bug | P1/Cx2 | 🔴 | 发布前安全收口 | `kernel/governance/rules_fmt.go` + `tools/codegen/contractgen/builder.go` | PR#376 F-SEC-002 |
| PR408-FU-PARSE-ERROR-DOUBLE-NIL-SWEEP-01 | **PR408 silent-skip 双 nil 漏网** — 现状: PR#408 round 4 sweep 用 `grep "return nil //nolint:nilerr"` 漏匹配双 nil 形式，`contract_spec_clients_test.go:114` + `no_deleted_auth_symbols_test.go:123` 仍能被不可解析文件绕过；修复: 两处 parseErr 分支改 `return nil, fmt.Errorf(...)` + 调用方 require.NoError；同时穷举所有 nil 变体 | bug | P1/Cx1 | 🔴 | — | `tools/archtest/contract_spec_clients_test.go` + `tools/archtest/no_deleted_auth_symbols_test.go` | PR#408 round 4 B4 sweep miss + 2026-05-07 review |
| PR408-FU-LEGACY-ANCHOR-BACKFILL-01 | **PR408 legacy `// INVARIANT:` 锚点回填** — 现状: 39 个 single-rule 文件用裸 `// <ID>:` 注释而非 `// INVARIANT: <ID>` 锚点，CLAUDE.md/doc.go 承诺的 `grep "INVARIANT: <ID>"` 直跳失效，inventory 脚本靠 fallback 兜底；修复: 每文件首行加锚点（多 ID 文件每条独立一行） + 删 inventory fallback 路径 + 新 archtest 守强制锚点必现 | refactor | P1/Cx2 | 🔴 | — | 39 个 `tools/archtest/*_test.go` + `scripts/audit/list-archtests.sh` + `tools/archtest/inventory_anchor_required_test.go`（新） | PR#408 round 2 P1 + 2026-05-07 review |
| PR408-FU-GOVERNANCE-OWNER-AST-EXTRACTION-01 | **Inventory governance section AST owner 提取** — 现状: `list-archtests.sh` 用 grep 抽 governance ID，引用关系被算成归属（rules_strict.go 写 `// 见 FMT-09` → FMT-09 同时归属 strict 与 strict_extra），开发者按 inventory 改错文件；修复: 改 go/ast 解析按 `Rule{ID:...}` struct literal 或 `const ruleID = "..."` 定位 canonical owner + inventory 加 referenced_by 列 | arch-opt | P1/Cx2 | 🟠 | 第二次主题归属错误 | `scripts/audit/list-archtests.sh` + `kernel/governance/rules_*.go` + `docs/audit/archtest-inventory.md` | 2026-05-07 review |
| PR408-FU-INVENTORY-GIT-LSFILES-01 | **Inventory 改 git ls-files** — 现状: `scripts/audit/list-archtests.sh` 用 find 扫工作树，未 commit 草稿/IDE 临时文件/.go.bak 污染 inventory，跨 worker/CI 漂移；round 3 已修 LC_ALL=C 但 file-source 残留；修复: 改 `git ls-files` + 顶部断言 `git rev-parse --is-inside-work-tree` | bug | P2/Cx1 | 🟢 | — | `scripts/audit/list-archtests.sh` + `hack/verify-archtest-inventory.sh` | PR#408 round 2 P2 残留 |
| PR408-FU-SCANNER-SHARED-FRAMEWORK-01 | **Archtest scanner 共享框架（根因修复）** — 现状: PR#408 4 轮 review 反复出现同类反模式（file-level skip / silent parse-error fallback / hardcoded scope / naming heuristic），per-file 一次性代码每个作者重发明 walk+parse+report 每次都有概率引入新变体（round 4 修 7 处单 nil，round 5 又发现 2 处双 nil）；修复: 抽 `tools/archtest/internal/scanner` 共享框架（fail-closed by construction、structured scope predicate、内置 vendor/testdata/worktrees skip、统一 receiver-type 解析），所有现有 scanner 渐进迁移；archtest 禁止 `tools/archtest/*_test.go` 直接 import `filepath.WalkDir/Walk` (also: cap-14) | arch-opt | P2/Cx4 | 🟡 | 第 N+1 轮 review 发现同类反模式 | `tools/archtest/internal/scanner/`（新） + 70+ 个 `tools/archtest/*_test.go` 渐进迁移 + 新 archtest `SCANNER-FRAMEWORK-USAGE-01` | PR#408 4 轮 review root-cause（2026-05-07） |

---

## cap-03: Contract 注册与发现

> 主要包：`kernel/wrapper` + `kernel/registry` + `pkg/contracts`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| P1-8 | **DEVICE-LIST-API** — 现状: `cells/devicecell/slices/devicelist/` 缺；修复: 新建 slice + `GET /api/v1/devices` 分页 + contract + contract_test | feat | P1/— | 🟡 | — | `cells/devicecell/slices/devicelist/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
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
| PR237-PM5 | **DUAL-LISTENER-DEPLOYMENT-GUIDE-01** — 现状: 缺双 listener 部署章节；修复: 新增 `docs/operations/dual-listener-deployment.md` | doc | Cx2 | 🟡 | — | `docs/operations/` | PR #237 round-2 PM-05 |
| PR237-PM7 | **EXAMPLE-INTERNAL-LISTENER-COMMENT-01** — 现状: examples/*/main.go 双 addr 缺注释；修复: 加注释或 `WithHTTPInternalDisable` | doc | Cx1 | 🟡 | — | `examples/*/main.go` | PR #237 round-2 PM-07 |
| LISTENER-API-SPEC-01 | **Listener API spec 化** — 现状: listener 选项散在代码；修复: contracts 化声明 | arch-opt | Cx2 | 🟡 | — | `contracts/http/` | PR#237 |
| ROUTE-ERROR-POLICY-01 | **Route error policy 统一** — 现状: 3+ route family 错误处理不一；修复: 定义共享 policy | arch-opt | Cx3-Cx4 | 🟠 | 3+ route 家族出现 | `runtime/http/` | systems review |
| T4 | **CB-RESILIENCE-PACKAGE-01** — 现状: Allower / CircuitBreakerRetryAfter 在 `runtime/http/middleware`；修复: 迁到 `runtime/resilience/circuitbreaker/` 独立包 (also: cap-x-cross) | refactor | — | 🟠 | 出现第 2 个非 HTTP CB 消费方 | `runtime/http/middleware/` + `runtime/resilience/circuitbreaker/` (新) | T4 |
| WM-32 | **mTLS 中间件** — 现状: 缺；修复: 加 TLS 构建器 + HTTP 证书提取钩子（折中：大规模环境 mTLS 卸载在 K8s/Service Mesh 解决，框架仅提供构建器） | feat | P2/Cx2 | 🟡 | V1.1 启动 | `runtime/http/middleware/` | backlog_later §7 WM-32（4/6 票）|
| B2-T-08 | **Config publish 失败码声明不完整** — 现状: contract 缺部分失败码声明；修复: 补 4xx/5xx 完整声明 | bug | P2/Cx1 | 🟡 | — | `contracts/http/config/publish/v1/contract.yaml` | backlog2 §8 B2-T-08 |

---

## cap-05: 身份认证 (Authn)

> 主要包：`runtime/auth` + `auth/refresh` + `auth/refresh/memstore` + `auth/config`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01 | **B5 follow-up PG runtime wiring + archtest 类型化** — 现状: corebundle 仍走 `WithInMemoryDefaults`；修复: B2 落 PG SessionRepository 后切真实 PG + archtest 升 packages-aware | refactor+test | P1+P2/Cx2+Cx3 | 🟠 | B2 落地 PG SessionRepository | `cmd/corebundle/access_module.go` + `cells/accesscore/cell_init.go` + `tools/archtest/` | PR#399 review L2 |
| ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 | **ACCOUNT-LOCKOUT-AUTO-LOCK-01** — 现状: sessionlogin 无失败次数累计 + 阈值 + auto-lock；修复: 完整业务设计 + PG schema + journey harness | feat | Cx3 | 🔴 | — | `cells/accesscore/slices/sessionlogin/` + user repo + integration test | PR-A63 复核 |
| CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 | **identitymanage 一致性等级误标** — 现状: 标 L0 实为 L1；修复: 校正 slice.yaml | arch-opt | Cx1 | 🔴 | — | `cells/accesscore/slices/identitymanage/slice.yaml` | systems layer review |
| OIDC-FAIL-FAST-DISCOVERY-01 | **OIDC discovery fail-fast** — 现状: discovery 错误不 fail-closed；修复: 引入 OIDC 时落地 | bug | Cx2 | 🟠 | 首个 prod OIDC 部署 | `adapters/oidc/` | systems layer review |
| OIDC-JWKS-ROTATION-WORKER-01 | **OIDC JWKS 轮转 worker** — 现状: provider cache 永不过期，IdP 轮换 JWKS 全员鉴权失败；修复: adapter 内置 `tokenRenewalWorker` + `cache_max_age` 头（fallback 24h）+ `ManagedResource.Worker()` | feat | P1/Cx2 | 🟡 | OIDC-FAILFAST-MR-COMPLETENESS (A-01) 落地 | `adapters/oidc/` | systems layer review + 030 §2 A-02 |
| PR-A8-RESIDUAL | **Vault K8s auth E2E** — 现状: Vault K8s auth 实现已落，缺真 K8s e2e；修复: 跑 testcontainers k8s 验证 | arch-opt | Cx2 | 🟡 | — | `adapters/vault/` | PR#305 |
| PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST | **登录 durable TX atomicity 集成测试** — 现状: 仅单元测；修复: PG session repo 落地后补 service-level test | test | Cx2 | 🟠 | PG session repo 落地 | `cells/accesscore/slices/sessionlogin/` | PR#338 |
| PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP | **AUTH-FAIL-CLOSED-DOC-CLEANUP-01** — 现状: nonce.go docstring + archive quickstart 未跟 PR-CFG-I 更新；修复: 补 deprecation banner | doc | P3/Cx1 | 🟡 | — | `runtime/auth/nonce.go` + `docs/archive/specs/201-wm2-key-rotation/quickstart.md` | PR#338 round-1 |
| PR267-FU-AUTHTEST-INTERNAL | **Auth test 内部化** — 现状: testHelpers 暴露过多；修复: internal package | arch-opt | Cx1 | 🟡 | — | `cells/accesscore/` | PR#267 |
| PR267-FU-ROLE-PREFIX-ADR | **Role prefix ADR** — 现状: role 命名前缀约定无 ADR；修复: 写 ADR | doc | Cx1 | 🟡 | — | `docs/architecture/` | PR#267 |
| X3 | **WM-36 SecureCookie key rotation** — 现状: 无密钥轮转；修复: 接入 rotation worker | feat | P3/— | 🟡 | — | `runtime/auth/` | WM-35 后续 |
| X5 | **P3-TD-11 accesscore domain 拆分** — 现状: domain 包过大；修复: User/Session/Role 拆分 | refactor | P3/— | 🟡 | X1 落地后 | `cells/accesscore/internal/domain/` | 历史 Batch 8 |
| X13 | **REFRESH-PARTITION-01** — 现状: 批量 DELETE GC；修复: `expires_at` range 分区 + DROP PARTITION (also: cap-10) | feat | P3/Cx2 | 🟠 | 生产流量达阈值 | migration + ops runbook | 通用 PG 模式 |
| T5 | **AUTH-SIGNER-01** — 现状: SigningKeyProvider 返回 `*rsa.PrivateKey`；修复: 改 `crypto.Signer` 支持 HSM/KMS/EC | arch-opt | — | 🟡 | caller 需 HSM/KMS | `runtime/auth/` | T5 |
| C-AC7 | **JWT jti claim 支持** — 现状: 缺 jti，单 token 无法黑名单撤销；修复: Issue() 加 jti + jti 黑名单存储 | feat | P2/Cx2 | 🟡 | 出现单 token 撤销需求 | `runtime/auth/` | backlog_later §6 C-AC7 |
| P3-TD-10 | **TOCTOU 竞态修复** — 现状: Phase 2 #54 session TOCTOU 未修；修复: Redis 分布式锁 + 持久化 session 稳定后处理 (also: cap-11) | bug | P2/Cx3 | 🟠 | post-v1.0 + Redis distlock 稳定 + PG session repo | `cells/accesscore/` | tech-debt-registry P3-TD-10 |
| P4-TD-03 | **IssueTestToken HS256 dead code** — 现状: 测试 helper 仍保留 HS256 路径，JWTVerifier 全拒；修复: 删 dead code 防误用 | refactor | Cx1 | 🟡 | — | `runtime/auth/` (test helper) | tech-debt-registry P4-TD-03 |
| SECURECOOKIE-AEAD-NEG-01 | **SecureCookie AEAD 负向测试** — 现状: AEAD 失败路径无测试；修复: 截断/伪造/边界长度/解密失败类型断言 (`errors.Is(err, ErrAEADAuthFailed)`) | test | Cx2 | 🟡 | v1.0 GA 前 | `pkg/securecookie/securecookie_test.go` | backlog1 §2.5 |
| B2-C-02 | **SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT** — 现状: setup 端点常驻 Public，未初始化窗口可被匿名首管抢注；修复: 移到 `/internal/v1/setup/` (service-token only) + contract `lifecycle: bootstrap`，或 1 次性 bootstrap token | feat | P0/Cx3 | 🔴 | — | `cells/accesscore/cell_routes.go:73` + `slices/setup/handler.go:46-58` + `contracts/http/auth/setup/admin/v1/contract.yaml:5` | backlog2 §1 B2-C-02 |

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
| B2-C-10 | **Auditappend 全局 mutex 串行化 13 topic** — 现状: 单 mutex 串行化所有 topic 处理；修复: 按 topic/分片细化锁 | bug | P1/Cx3 | 🟡 | 容量/吞吐压力出现 | `cells/auditcore/slices/auditappend/service.go:93,165` | backlog2 §4 B2-C-10 |

---

## cap-09: 配置加载与热更新

> 主要包：`runtime/config` + watcher + `cells/configcore`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR-CFG-A-DEFER-2 | **ConfigCore L2 divergence** — 现状: L2 与 L1 表项 schema 偏差；修复: 收口 | arch-opt | Cx1 | 🟡 | — | `cells/configcore/` | PR#268 |
| CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 | **ConfigCore 缓存生命周期归属** — 现状: 内存增长信号；修复: 明确 owner + 清理 | arch-opt | Cx2 | 🟠 | 出现内存增长信号 | `cells/configcore/` | systems layer review |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | **ConfigReceive placeholder 清理** — 现状: `accesscore/configreceive` 占位 per ADV-05，被钉占位让规则不报错；修复: configcore 的 entry-upserted/deleted contract 标 `lifecycle: draft` 直到真有消费动机；删 configreceive；不维持占位绕过治理 | refactor | P1/Cx2 | 🟡 | K-06 落地后 | `cells/accesscore/slices/configreceive/` | systems layer review + 030 §2 C-01 |
| PR-CFG-G1-FU6 | **ConfigCore G1 follow-up 6** — 现状: PR-CFG-G1 余项；修复: 单独跟进 | arch-opt | Cx2 | 🟡 | — | `cells/configcore/` | PR-CFG-G1 |
| PR320-FU-CONFIGCORE-CI-NOOP | **ConfigCore CI noop test** — 现状: noop publisher CI 路径未覆盖；修复: 加测 | test | P3/Cx1 | 🟡 | — | `cells/configcore/` | PR#320 |
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
| B2-A-28 | **Redis password 可选 fail-open** — 现状: 缺 password 仍允许连接；修复: real mode 强制 password fail-fast | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `adapters/redis/client.go:62-68` | backlog2 §5.3 B2-A-28 |
| B2-C-12 | **Audit HMAC key 最小长度未验证** — 现状: 任意短密钥都接受；修复: 加 32 字节最小长度 + Validate | bug | P2/Cx1 | 🟡 | 发布前安全收口 | `cells/auditcore/cell.go:319` | backlog2 §4 B2-C-12 |

---

## cap-11: 分布式锁

> 主要包：`runtime/distlock` + `adapters/redis`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
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

---

## cap-13: 可观测性

> 主要包：`runtime/observability/{metrics,tracing,poolstats}` + `pkg/logutil` + `adapters/{prometheus,otel}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| ADAPTER-MANAGED-RESOURCE-COMPLETENESS-01 | **Adapter readyz probes 完整性** — 现状: 部分 adapter 缺 ready probe；修复: 统一规范 | arch-opt | Cx2 | 🟡 | — | `adapters/{postgres,redis,s3}/` | systems layer review |
| R3 | **safe_observe DI** — 现状: observe DI 路径未统一；修复: 抽象统一 | arch-opt | — | 🟡 | — | `runtime/observability/` | R3 |
| A5a-R3 | **Observability ctx 透传** — 现状: 部分路径丢 ctx；修复: thread ctx | arch-opt | — | 🟡 | — | `runtime/observability/` | A5a |
| A5a-R12 | **Observability 集成补全** — 现状: integration test gap；修复: 加测 | test | — | 🟡 | — | `runtime/observability/` | A5a |
| OBS-SSA-ANALYZER-01 | **OBS SSA analyzer** — 现状: 缺静态分析；修复: 加 SSA-based analyzer | arch-opt | Cx3 | 🟡 | — | `tools/archtest/` + `runtime/observability/` | OBS-SSA |
| PR-CI-5-FU-HEALTH-LATE-WATCHER | **Health late watcher** — 现状: late watcher 路径未覆盖；修复: 补 | arch-opt | Cx2 | 🟡 | — | `runtime/http/health/` | PR-CI-5 |
| PR392-FU-AUDIT-CHAIN-WIRING | **BOOTSTRAP-AUDIT-CHAIN-WIRING-01** — 现状: onAuthFail 用 slog 未接 audit chain；修复: 升级为 audit.AppendBootstrapAuthFail | arch-opt | P2/Cx2 | 🟠 | accesscore audit chain cross-cell wiring | `cmd/corebundle/access_module.go` | PR #392 ADR §D10 |
| PR237-OB2 | **Listener observability** — 现状: per-listener 观测 metric 不全；修复: 补 | arch-opt | Cx2 | 🟡 | — | `runtime/observability/` | PR#237 |
| PR284-FU-COMPOSE-HEALTH | **Compose health** — 现状: docker-compose health 不全；修复: 补 healthcheck | arch-opt | Cx2 | 🟡 | — | `examples/*/docker-compose.yml` | PR#284 |
| PR283-OTEL-SLOG-ERROR-ATTR | **OTEL-SLOG-ERROR-ATTR-NORMALISE-01** — 现状: `slog.Any("error", err)` 在 OTEL bridge 会展开 struct；修复: ReplaceAttr hook 序列化 err.Error() | arch-opt | P2/Cx2 | 🟠 | 首次 OTEL slog bridge 接入 | `adapters/otel/` + `runtime/observability/logging/` | PR#283 round-2 I3 |
| M1-OBSERVED | **HEALTHZ-INTERFACE-PACKAGE-01** — 现状: 38 处 Health 实现分散无统一接口；修复: 新建 `kernel/healthz` 接口包 (Aggregator/Probe/Snapshot) + codegen 从 cell.yaml 派生状态 schema + 默认 `runtime/observability/healthz/inmemory` 实现 + 可选 postgres/otel adapter + `HEALTHZ-WRITE-01` archtest + 38 处分散 Health 收口（不持久化 yaml，持久化交宿主） (also: cap-14, cap-10) | feat | P2/Cx3 | 🟡 | — | `kernel/healthz/` (新) + `runtime/observability/healthz/` + `tools/codegen/` | ADR-202605041430 M1 |
| P4-TD-10 | **Metrics path label cardinality** — 现状: `r.URL.Path` 直接作 label，参数化路由展开成高基数序列（`/users/123` `/orders/42`...）；修复: 改用 chi route template 或 `_` 占位 | bug | P2/Cx2 | 🟡 | — | `runtime/observability/metrics.go` | tech-debt-registry P4-TD-10 |
| WS-DX-01 | **WS per-conn context tracing** — 现状: per-conn ctx 基于 Background()，无 tracing/correlation 传到 MessageHandler；修复: 透传 tracing ctx | arch-opt | Cx2 | 🟡 | observability 接入时 | `runtime/websocket/` | tech-debt-registry WS-DX-01 |
| B2-C-01 | **Audit hashchain 重启未恢复尾节点** — 现状: NewHashChain 启动从空链开始，多实例或重启后尾哈希不连续；修复: cell 启动时从 repo `SELECT last hash` 注入；考虑 leader 单写或 advisory lock | arch-opt | P0/Cx4 | 🔴 | — | `cells/auditcore/internal/domain/hashchain.go:31` + `cells/auditcore/cell.go` | backlog2 §1 B2-C-01 |
| B2-R-05 | **OTel metric provider ctx 固定 Background** — 现状: provider 用 ctx.Background()；修复: 透传 caller ctx | bug | P1/Cx4 | 🟡 | — | `adapters/otel/metric_provider.go:174,178,185` | backlog2 §3 B2-R-05 |
| B2-R-06 | **OTel tracer provider 未注册全局** — 现状: tracer 实例化后未 SetGlobal；修复: SetTracerProvider | bug | P1/Cx2 | 🟡 | — | `adapters/otel/tracer.go:56,73` | backlog2 §3 B2-R-06 |
| B2-R-07 | **OTel tracer shutdown 无 deadline** — 现状: shutdown 无超时上限；修复: 加 ctx deadline | bug | P1/Cx1 | 🟡 | — | `adapters/otel/tracer.go:63,65` | backlog2 §3 B2-R-07 |
| B2-R-08 | **OTel callback 需手工 unregister** — 现状: callback 注册后无自动 unregister；修复: 接 lifecycle hook | bug | P1/Cx3 | 🟡 | — | `adapters/otel/pool_collector.go:43,110` | backlog2 §3 B2-R-08 |
| B2-R-09 | **OTel attr cache key 碰撞无上界** — 现状: attr cache 无 LRU/eviction；修复: 加 LRU + max size | bug | P1/Cx3 | 🟡 | — | `adapters/otel/metric_provider.go:84,96,101` | backlog2 §3 B2-R-09 |
| B2-C-05 | **Auditappend actor 缺失降级不安全** — 现状: actor 缺失时静默降级；修复: fail-closed | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `cells/auditcore/slices/auditappend/service.go:133` | backlog2 §4 B2-C-05 |
| B2-C-09 | **Auditquery raw payload 直接回传** — 现状: handler 直接回传 raw payload 含敏感字段；修复: redact + slog level 区分 | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `cells/auditcore/slices/auditquery/handler.go:35,42` | backlog2 §4 B2-C-09 |
| B2-C-14 | **Hash-chain 跨重启连续性测试缺** — 现状: 缺重启场景验证；修复: 加 testcontainer 重启回归 | test | P2/Cx2 | 🟡 | — | `cells/auditcore/slices/auditappend/service_test.go:110` | backlog2 §4 B2-C-14 |
| B2-A-20 | **OTel simple tracer propagation 不对称** — 现状: 解析 vs 注入实现不对称；修复: 统一 propagator | bug | P2/Cx2 | 🟡 | — | `runtime/observability/tracing/tracer.go:77` | backlog2 §5.3 B2-A-20 |
| B2-A-22 | **Prometheus handler 无 timeout** — 现状: scrape 无超时控制；修复: 加 server.WriteTimeout | bug | P1/Cx1 | 🟡 | — | `cmd/corebundle/metrics.go:83` | backlog2 §5.3 B2-A-22 |
| B2-A-23 | **Prometheus cellID label 无验证** — 现状: cellID label 接受任意字符串；修复: 加 enum/格式校验 | bug | P1/Cx1 | 🟡 | — | `adapters/prometheus/hook_observer.go:114-117` | backlog2 §5.3 B2-A-23 |
| B2-A-24 | **Prometheus race test 缺** — 现状: provider 缺并发竞争测试；修复: 加 race | test | P1/Cx2 | 🟡 | — | `adapters/prometheus/metric_provider_test.go` | backlog2 §5.3 B2-A-24 |
| REPO-HEALTHCHECKER-01 | **configcore/auditcore repo 接 HealthCheckers** — 现状: HealthCheckers 仅接 outbox，关键 repo 未接探针；修复: 接入 cell HealthCheckers（与 PR-CFG-1 PG relay probe 同主题）| arch-opt | P1/Cx2 | 🟡 | 与 PR-A53 同 PR | `cells/configcore/cell.go` + `cells/auditcore/cell.go` | backlog1 §3 |

---

## cap-14: 代码生成与治理工具链

> 主要包：`tools/{archtest,codegen,depgraph,e2egate,metricschema,generatedverify}` + `cmd/gocell` 8 子命令

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| K05-ARCHTEST-PACKAGES-LOAD-UPGRADE | **K05-ARCHTEST-PACKAGES-LOAD-UPGRADE** — 现状: archtest AST 仅按 `reg` 字面 receiver 匹配，rename 可绕过；修复: 升 packages.Load + 按 cell.Registry 类型判断 | arch-opt | Cx3 | 🟠 | K#06 contractgen 类型分析 | `tools/archtest/codegen_unified_test.go` | K#05 PR #365 review K05-07 |
| TEST-JOURNEY-ROOT-HARNESS-01 | **ROOT-JOURNEY-INTEGRATION-HARNESS-01** — 现状: J-useronboarding 等 root journey 缺真 Go integration harness；修复: 补 tests/integration/ | test | Cx3 | 🔴 | — | `tests/integration/` + `journeys/J-*.yaml` | PR-A63 复核 |
| V-A11 | **GOVERNANCE-EXAMPLES-COVERAGE-01** — 现状: governance rules 不扫 examples/；修复: 加 rules_examples.go | arch-opt | Cx3 | 🔴 | — | `kernel/governance/rules_examples.go` (新) | verification §A11 |
| V-A13 | **GENTPL-LIFECYCLE-PATTERN-01** — 现状: gentpl/main.go.tpl 直连 app.Start/Stop 跳过 phase3b（PR#392 已删 phase3b admin provision）；修复: 决定模板"最小骨架 vs 开箱即用" + 集成测试 | doc+arch-opt | Cx1+Cx2 | 🟡 | — | `kernel/assembly/gentpl/main.go.tpl` | PR #243 review §E1 |
| PR245-F6 | **OUTBOX-ARCHTEST-SCAN-SCOPE-EXPAND-01** — 现状: isCellFile 仅匹配 `cell.go`；修复: 改为 `cells/<n>/*.go` 排除 internal/slices/test | arch-opt | Cx2 | 🟡 | — | `tools/archtest/outbox_cell_test.go` | PR#245 round-1 F-6 |
| PR245-F10 | **CELL-RAW-DEPS-ARCHTEST-EXPAND-01** — 现状: PR-A5c 仅 ban WithPublisher/WithOutboxWriter；修复: 一并 ban 所有 raw-dep Option（029 #13 PR-A22 / 030 G-17 吸收） | arch-opt | Cx2 | 🟢 | — | `tools/archtest/raw_deps_test.go` (或扩展) | PR#245 round-1 F-10 |
| PR250-F3 | **Event wire byte pinning** — 现状: 缺 byte 级回归；修复: 加 pinning test | test | Cx2 | 🟡 | — | `cells/accesscore/` | PR#250 |
| JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 | **Journey contract 存在性校验** — 现状: journey 引用 contract 不存在不报错；修复: 加 governance 规则 | arch-opt | Cx2 | 🔴 | — | `kernel/governance/` | systems layer review |
| ASSEMBLY-SCAFFOLD-CMD-01 | **ASSEMBLY-SCAFFOLD-CMD-01** — 现状: 029 K#10 PR#404 已完成 (a) AssemblyMeta + (c) modules_gen.go 派生；残余 = (b) `gocell scaffold assembly --id=... --cells=... --deploy=k8s` | feat | P1/Cx2 | 🟠 | 加第 2 个 assembly | `cmd/gocell/app/scaffold_assembly.go` (新) | systems-layer-07 §P1-3 + 030 §2 K-08 |
| B2-K-08-CARVEOUT-NARROW | **B2-K-08-CARVEOUT-NARROW** — 现状: errcode_constructor_test 对 ctxcancel/httputil 做 file-level 豁免；修复: 改 function-level + 扩展 message const | arch-opt | P1/Cx2 | 🟡 | 第 3 个 file 豁免出现 | `tools/archtest/` + `pkg/ctxcancel/` + `pkg/httputil/` | PR#391 K#08 carve-out |
| JOURNEY-STATUS-BOARD-LIFECYCLE-CONSISTENCY-01 | **Journey status-board 状态机一致性** — 现状: board state 与 yaml lifecycle 双状态机各表；修复: 定义强映射 + 双向校验 | arch-opt | P1/Cx2 | 🟡 | 第 9 条 journey 写 board 时 | `kernel/governance/rules_journey.go` + status-board + J-*.yaml | systems-layer-06 §P1-4+5 |
| IDUTIL-UUID-RAND-FAILURE-TEST-01 | **UUID rand failure test** — 现状: rand.Read 失败路径无回归；修复: fault injection test | test | Cx1 | 🟡 | — | `pkg/idutil/` | GitHub #23 |
| FU2-GOVERNANCE-STATIC | **Governance static analysis** — 现状: typed gate (→ PR#321) 已落，static 后续；修复: 跟进 | arch-opt | Cx3 | 🟢 | — | `tools/archtest/` | — |
| PR266-AUDITAPPEND-STRICT | **AUDITAPPEND-STRICT-UNMARSHAL-01** — 现状: `cells/auditcore/slices/auditappend/service.go:102` 用宽松 json.Unmarshal 接受未知字段，与 audit 合规"严格记录"语义存在张力；当前是 best-effort by design（lenient path 让事件不丢）；修复方向: (a) 注释加强说明 lenient 取舍；(b) strict 模式 toggle 给将来需要严格审计的 deployment；(c) 永久错误路径（PermanentError → DLX）的 unmarshal 失败行为与 lenient 区分；(b)(c) 待第一个 strict-audit 客户出现实施 | arch-opt | P2/Cx2 | 🟡 | strict-audit 客户出现 | `cells/auditcore/slices/auditappend/service.go` | PR#266 round-2 OPS reviewer F-OPS-03 |
| PR332-VERIFY-GENERATED-REMEDIATION-DRIFT-01 | **Verify codegen drift remediation 提示** — 现状: drift 报错不提示修复命令；修复: 补 hint | arch-opt | Cx2 | 🟡 | — | `cmd/gocell/` | PR#332 |
| VERIFY-CODEGEN-SANDBOX-INTEGRATION | **VERIFY-CODEGEN-SANDBOX-INTEGRATION** — 现状: --local=false sandbox 路径无端到端回归；修复: 补 1-2 条 git worktree integration test | test | Cx2 | 🟠 | 修改 verify-codegen-*.sh 或 runVerifyCodegen* | `cmd/gocell/app/codegen_*_drift_test.go` + tools/codegen helper | PR #404 K#10 review P2 |
| F2 | **SYSTEM-TOPOLOGY-API** — 现状: 缺 `GET /internal/v1/system/topology`；修复: 基于 `kernel/registry` 返回 cell/slice/contract 拓扑 JSON | feat | P3/Cx2 | 🟡 | — | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |
| NOLINT-AUDIT-01 | **Nolint audit** — 现状: 全仓 101 处 nolint 含 errcheck 类豁免；修复: 审查 | arch-opt | Cx2 | 🟡 | — | 全仓 *.go | NOLINT-AUDIT-01 |
| ADR-INDEX-01 | **ADR index** — 现状: 缺 ADR 索引；修复: 生成 docs/architecture/INDEX.md | doc | Cx1 | 🟡 | — | `docs/architecture/` | ADR-INDEX-01 |
| TEST-CHDIR-PARALLEL-CLI-01 | **TEST-CHDIR-PARALLEL-CLI-01** — 现状: 4 个 CLI test 用 os.Chdir 阻碍 t.Parallel()；修复: 抽 RootResolver helper | test | P3/Cx2 | 🟡 | CLI 测试 > 30s 或新 generate sub-cmd | `cmd/gocell/app/generate_*_test.go` + `verify_codegen_*_test.go` | PR #361 round-2 #3 |
| T6 | **CONTRACT-EVENT-PAYLOAD-CODEGEN-01** — 现状: scaffold/generate 无 schema → Go 能力；修复: 派生 payload.gen.go + decode/validate helper | feat | — | 🟠 | event subscriber decode 扩散 ≥5 cell | `tools/codegen/eventgen/` (新) + `generated/contracts/event/` | T6 |
| T7 | **CH-05 alias eval** — 现状: import alias / const eval 漂移；修复: governance 加 | arch-opt | — | 🟡 | import alias / const drift | `kernel/governance/` | T7 / PR-A45 |
| T10 | **Devtools cell promotion** — 现状: catalog 内置；修复: 升级为外部 cell | arch-opt | — | 🟠 | catalog customization | `cells/devtools/` + `runtime/` | T10 / PR-A37 |
| M4-COVERAGE | **REVERSE-COVERAGE-ARCHTESTS-01** — 现状: 缺 5 条反向追溯规则；修复: 加 `IMPL-DECL-COVER-01` (cell 间 Go import 必须经 contract，非 slice 间) + `HANDLER-DECL-COVER-01` (http handler 必须出现在某 contract.yaml) + `EMIT-DECL-COVER-01` (outbox emit 必须出现在 contract.triggers) + `DEAD-CONTRACT-01` (active contract 必须有 handler 入口) + `DEAD-CODE-01` (deprecated contract 引用代码不能在 main 分支)；不含 SLICE-DECOUPLE | arch-opt | P2/Cx3 | 🟠 | M3 落地 | `tools/archtest/` | ADR-202605041430 M4 |
| CONTRACT-BREAKING-01 | **`gocell check contract-breaking`** — 现状: 缺 API schema 历史破坏性变更比对；修复: 借鉴 buf.build 引入 40+ 条规则（字段删除/必填放宽阻断） | feat | P2/Cx3 | 🟡 | V1.1 启动 | `cmd/gocell/` + `kernel/governance/` | backlog_later §5 |
| CONTRACT-CODEGEN-01 | **Go DTO ↔ JSON Schema 双向推断** — 现状: 代码与契约 YAML 分裂；修复: Struct Tags 实时双写到 JSON Schema（对齐 oapi-codegen） | feat | P2/Cx3 | 🟡 | V1.1 启动 | `tools/codegen/` + DTO 模板 | backlog_later §5 |
| CONTRACT-STUB-01 | **Consumer-Driven Contract Stub** — 现状: 缺消费方 stub 校验；修复: 提供 Stub 桩代码套件（对标 Spring Cloud Contract / Pact） | feat | P2/Cx3 | 🟡 | V1.1 启动 | `tools/contracttest/` | backlog_later §5 |
| C-L6 | **Contract ID 解析标准统一** — 现状: CLI 用点分（http.auth）、Generator 退化为斜杠分割，开发者上下文脱节；修复: 全局检索 + 统一内部 Contract ID 解析 | bug | P2/Cx2 | 🟡 | — | `cmd/gocell/` + `kernel/scaffold/` + `tools/codegen/` | backlog_later §6 C-L6 |
| CONTRACTTEST-SCHEMAREF-FAILFAST-01 | **contracttest schemaRefs 默认 fail-fast** — 现状: 未命中 schemaRefs key 默认 no-op，掩盖测试缺失；修复: 默认 fail；宽松改显式 `WithMissingKeyTolerated()` API | arch-opt | P1/Cx2 | 🟡 | 发布前必做 | `pkg/contracttest/contracttest.go` | backlog1 §2.2 |
| CONTRACT-ENDPOINT-TEST-MAPPING-01 | **active contract → 测试用例映射门禁** — 现状: 缺活跃端点 → 测试覆盖映射；修复: governance 加规则：`lifecycle: active` HTTP contract 必须有对应 contract test | arch-opt | P1/Cx2 | 🟡 | 发布前必做 | `kernel/governance/` | backlog1 §2.2 |
| CONTRACT-PATH-QUERY-EXECUTABLE-01 | **path/query 参数约束可执行测试** — 现状: pattern/min/max/format 无入参可执行测试；修复: 加 transport 入参 rejected 用例覆盖 | arch-opt | P1/Cx2 | 🟡 | 发布前必做 | `pkg/contracttest/contracttest.go` | backlog1 §2.2 |
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
| ARCHTEST-CARVEOUT-NARROW-FUNCLEVEL | **Carve-out 收窄到 function-level + ADR 登记** — 现状: ERRCODE-KIND-LITERAL-01 / MESSAGE-CONST-LITERAL-01 给 `pkg/ctxcancel/` + `pkg/httputil/` file-level 豁免，无 ADR；与现有 `B2-K-08-CARVEOUT-NARROW` 同根但更细：(a) 改 function-level（仅豁免 `WrapOrInfra` / `writeErrcodeError` struct literal 行）+ (b) 新 ADR 登记 carve-out 列表+理由 | arch-opt | P1/Cx2 | 🟡 | 与 B2-K-08 同 PR | `tools/archtest/errcode_constructor_test.go` + ADR (新) | PR#391 F2 |
| ARCHTEST-CONTRACTSPEC-LITERAL-RUNTIME | **NO-MANUAL-CONTRACTSPEC-LITERAL-01 扫描 runtime** — 现状: archtest 仅扫 `cells/` + `examples/`，`runtime/` 漏；新加 spec literal 不报；修复: 扫描根加 `runtime/`（保留 framework infra 必要的豁免列表）| arch-opt | P1/Cx1 | 🟡 | — | `tools/archtest/no_manual_contractspec_literal_test.go:97` | PR#376 F-ARCH-001 |
| ARCHTEST-CELL-METADATA-FIELD-DRIFT | **cell.yaml ↔ cell_gen.go 字段级漂移守卫** — 现状: K#05 NO-METADATA-LITERAL-IN-CELLGO-01 / MARKER-WIRE-SINGLE-SOURCE-01 守结构一致，**字段级（owner / Schema.Primary / VerifySmoke）漂移仍可能发生**；修复: archtest 扫 3 cell 的 cell.yaml 与 cell_gen.go 对应字段值是否一致 | test | P1/Cx2 | 🟡 | — | `tools/archtest/` + 3 cell `cell_gen.go` | systems-layer-04 cells P0 |
| CATALOG-DTO-DRIFT-ARCHTEST | **metadata→catalog DTO 完整性映射 archtest** — 现状: PR#404 加 Owner/MaxConsistencyLevel 已同步进 catalog wire，但**无 archtest 守卫**；修复: 写 archtest 校验 AssemblyMeta 字段必映射到 `runtime/devtools/catalog/wire.go` DTO | test | P2/Cx2 | 🟡 | — | `tools/archtest/` + `runtime/devtools/catalog/wire.go` | PR#404 F4 (resolved 但缺守卫) |
| ADR-DATE-CONSISTENCY-CHECK | **ADR 文件名日期 vs 内容 Date 一致性** — 现状: PR#404 ADR `202605061800-...md` 文件内 Date: 2026-05-07（1 天误差）；修复: archtest 校验 `docs/architecture/yyyymmddHHmm-*.md` 文件名前缀日期 = 内容 `Date:` 字段日期 | test | P3/Cx1 | 🟡 | — | `tools/archtest/` + ADR 命名约定 | PR#404 F6 |

---

## cap-x-cross: 横切

> 不属于单一 capability 的项：CI / lint baseline、跨 capability 大重构（≥ 4 cap 且无明确 owner）、仓库级文档、发布相关 checklist。

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR-BATCH2-RETRO-FU | **Batch2 retrospective 收口** — 现状: 多个跨 cap 发现；修复: 拆条 fix-up | arch-opt | Cx1-Cx2 | 🔴 | — | `runtime/auth/` + `cells/` | batch2 retrospective |
| ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 | **ADAPTER-ERROR-CLASSIFICATION-TRANSIENT** — 现状: postgres/redis/s3 错误码不分 transient/permanent，consumer 无法做退避决策；修复: 复用 `errcode.WrapInfra` + `errcode.IsTransient`，PG `40001/40P01/08*`、Redis `i/o timeout`、S3 5xx/429 标 transient，其余永久 | arch-opt | P1/Cx3 | 🟠 | 第 1 个 handler disposition 收口 | `adapters/{postgres,redis,s3}/errors.go` + `pkg/errcode/` | systems layer review + 030 §2 A-03 |
| ADAPTER-FAKE-EXPORT-01 | **Adapter fake export 一致性** — 现状: adapter fake 仅 `_test.go` white-box，cells 测试只能自写 fake 或 import adapter（破 LAYER-04）；修复: 每个对外接口 adapter 开 `adapters/<name>/<name>fake/` 子包导出 `NewFakeClient/NewMemKeyProvider`（参考 `runtime/eventbus` in-mem 范式）| arch-opt | P1/Cx3 | 🟠 | cell mock 扩展 | `adapters/*/fake/` (新子包) | systems layer review + 030 §2 A-04 |
| PR-A41-FU1 | **PR-A41 advisory rules follow-up** — 现状: governance advisory 规则余项；修复: 跟进 | arch-opt | Cx2 | 🟡 | — | `kernel/governance/` | PR-A41 |
| PR237-F06 | **DUAL-LISTENER-DEVMODE-WARN-TEST-01** — 现状: PR-A14a 留尾，`cmd/corebundle/shared_deps.go::internalGuardFromEnv` 在 dev 模式（`GOCELL_ADAPTER_MODE=""`、未设 `GOCELL_SERVICE_SECRET`）返回 nil guard 并 `slog.Warn`，无测试断言 Warn 触发；修复: 加 `internal_guard_env_test.go` table-driven 覆盖 dev/real × 有/无 SERVICE_SECRET 四象限 | test | Cx1 | 🟡 | — | `cmd/corebundle/internal_guard_env_test.go` (新) | PR#237 reviewer F-06 |
| PR237-DX1 | **LISTENER-OPTION-NAMING-UNIFY-01** — 现状: PR-A14a 选项前缀不对称，`WithHTTPPrimaryAddr` / `WithHTTPInternalAddr`（带 HTTP）与 `WithPrimaryListener` / `WithInternalListener`（不带 HTTP），IDE 补全时两组不相邻；修复: 统一为 `WithHTTP*Listener` 或去掉 addr 侧 HTTP 前缀；当前 gocell 自身无外部调用方，可任意时间一次改名 | refactor | Cx2 | 🟡 | — | `runtime/bootstrap/bootstrap.go` + 测试 + `cmd/corebundle` + `examples/*/main.go` | PR#237 第二轮 reviewer DX F3 |
| PR237-A4 | **Listener architecture** — 现状: 双 listener 架构 doc 缺；修复: 写架构说明 | arch-opt | Cx2 | 🟡 | — | `runtime/http/` | PR#237 |
| PR238-FU4 | **CONFIGREPO-LEGACY-NOTFOUND-TEST-DEDUP-01** — 现状: `config_repo_test.go` 中 `TestConfigRepository_GetByKey_NotFound` (line ~172) 与 `TestConfigRepository_GetVersion_NotFound` 用 `assert.AnError` 测的是 other-error 分支，与 `TestGetByKey_OtherScanError_ReturnsErrConfigRepoQuery` / `TestGetVersion_OtherScanError_ReturnsErrConfigRepoQuery` 重复，造成 mutation-test 误导；修复: 删除两个 legacy 命名函数或重构为 table 行 | test | P3/Cx1 | 🟡 | — | `cells/configcore/internal/adapters/postgres/config_repo_test.go` | PR#238 L4 reviewer T-04 |
| PR280-FU1 | **CHANGEPASSWORD-CONCURRENT-SEMANTICS-01** — 现状: `cells/accesscore/slices/identitymanage/service.go::ChangePassword` 旧密码 bcrypt 校验在 RunInTx 之外，新 hash 写入在事务内；并发改密用同一旧密码均通过事务外校验，写入新 hash 时无 CAS 保护，后到者覆盖先到者；IssueForUser 也在事务外，进一步放大语义模糊；修复方向 menu: (A) 闭包顶部重读 user 二次 bcrypt（双倍成本）/ (B) `users.password_version` 列 + `UPDATE ... WHERE id=? AND password_version=?` CAS（Keycloak 模式，需 migration）/ (C) 接受当前语义，contract+SDK 明示并发后到者生效；对标: Keycloak 实体版本、Ory Kratos password settings hook、Supabase Auth 事务内收敛 | arch-opt | P2/Cx3 | 🟡 | 客户端反馈不可预测或安全审查要求 CAS | `cells/accesscore/slices/identitymanage/service.go` + 选项 B 时 `cells/accesscore/internal/domain/user.go` + adapters/postgres user_repo + migration | PR#280 六席位审查（3/6 OUT_OF_SCOPE 共识） |
| DEVOPS-INTEGRATION-CLEANUP-WAIT-TIMEOUT-01 | **Devops integration cleanup wait timeout** — 现状: e2e cleanup 超时；修复: 加 wait helper | arch-opt | Cx1 | 🟡 | — | `tests/e2e/` | GitHub #19 |
| X4 | **WM-7 泛型 BulkResult** — 现状: 各 cell 各写 BulkResult；修复: 抽泛型 | feat | P3/— | 🟡 | — | `pkg/` | 历史 Batch 8 |
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
| B2-C-13 | **L2 跨层 e2e 回归不足** — 现状: setup → audit → config 跨 cell e2e 不全；修复: 加跨 cell integration test | test | P2/Cx3 | 🟡 | — | `cells/accesscore/slices/setup/service_test.go` + `tests/integration/` | backlog2 §4 B2-C-13 |
| B2-T-07-FU-4 | **SVCTOKEN 跨信任域限制** — 现状: 跨 trust domain 时 SVCTOKEN 无额外限制；修复: 加 trust domain claim + 验证（A5 follow-up） | arch-opt | Cx4 | 🟠 | 多租户/跨信任域需求 | `contracts/` + `runtime/auth/` | backlog2 §8 A5 follow-up |
| ADAPTER-CONNECT-BUDGET-01 | **adapter 级 ConnectTimeout 强制** — 现状: 各 adapter 依赖上层 ctx；修复: adapter 级 ConnectTimeout（默认 5s）写 Config + Validate + `ERR_ADAPTER_CONNECT_TIMEOUT` (also: cap-08, cap-10；PG 部分由 PR#401 已部分覆盖) | bug | P1/Cx2 | 🟡 | v1.0 GA 前 | `adapters/rabbitmq/connection.go` + `adapters/postgres/pool.go` | backlog1 §2.4 |
| S3-FAILURE-INJECTION-01 | **S3 故障注入测试** — 现状: 缺 MinIO testcontainer 集成测；修复: 上传 403/5xx/timeout/recovery 路径覆盖 (also: cap-13) | test | P1/Cx2 | 🟡 | v1.0 GA 前 | `adapters/s3/s3_test.go` | backlog1 §2.5 |
| SWEEPER-OBSERVABLE-01 | **Sweeper onError + 并发度 + 构造期 fail-fast** — 现状: (a) `Sweeper.OnError=nil` 时 sweep 失败完全沉默，并发度按 finding 数计算不准；(b) Sweeper 用公开字段 + `Start()` 运行时 nil 检查，与 fail-fast 构造器约定不一致；修复: `runTick` 错误分支补 `slog.Error` + `command_sweep_errors_total{cell}` counter + onError 注入 + 并发度按 `groups × capacity × cost` 计算 + `NewSweeper(scanner, queue, clk, ...)` 构造器构造期 fail-fast (also: cap-08, cap-13；与 PR252-F2 同 PR) | bug | P1/Cx2 | 🟠 | 与 PR252-F2 同 batch | `kernel/command/sweeper.go` | backlog1 §3 + 030 §3 G-09 |

---

## 030 待分类（pending — 待 review 后归类入 cap）

> 来源：[`docs/plans/202605051600-030-review-0504-implementation.md`](plans/202605051600-030-review-0504-implementation.md) 中 OPEN items。  
> 已剔除：dedup（C-03/C-08/G-04/G-16/G-17/J-01/F-10 与现有 backlog 重复）+ 已 ✅（K-01/A-05/A-06/G-01/G-02/G-03）+ won't-do（K-04/K-05）+ 吸收（A-07/A-08→A-01；C-07→C-04；G-05→R-01）。  
> 已合并到现有条目（描述补全后删除 030 行，2026-05-07 第二轮）：K-07→CELLS-SLICE-MULTI-VERB-DECOMPOSE-01 / K-08→ASSEMBLY-SCAFFOLD-CMD-01 / A-02→OIDC-JWKS-ROTATION-WORKER-01 / A-03→ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 / A-04→ADAPTER-FAKE-EXPORT-01 / C-01→CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 / G-09→SWEEPER-OBSERVABLE-01（含构造期 fail-fast）/ F-08→M2-LIFECYCLE（含状态机显式化）。  
> 「建议 cap」列由本次分析推断，等用户确认后正式迁入对应章节。

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source | 建议 cap |
|---|---|---|---|---|---|---|---|---|
| K-02 | **JOURNEY-LIFECYCLE-CI-CLOSE** — (a) 升 J-ssologin 为 active；(b) `runner.RunActiveJourneys` active 集为空时 fail；(c) `gocell validate` 增 `journey.contracts ↔ contracts/` 双向存在性校验（对偶 ADV-06）；J-confighotreload 引用未声明 `event.config.entry-deleted.v1` | feat | P0/Cx2 | 🔴 | — | `journeys/J-*.yaml` + `kernel/governance/` + `kernel/verify/` | 030 §2 K-02 | cap-14 |
| K-03 | **KERNEL-OBSERVABILITY-PKGDOC** — kernel/observability 无包级 doc.go，与 runtime/observability 职责切分不明；修复: 加 30-50 行 doc.go 明确 provider-neutral 抽象 | doc | P1/Cx1 | 🟡 | — | `kernel/observability/doc.go` (新) | 030 §2 K-03 | cap-13 |
| R-01 | **EVENT-OBSERVABILITY-METRIC-PACK**（吸收 G-05）— (a) RelayCollector 不被 bootstrap 自动注入；(b) eventrouter 无 collector；(c) InMemoryEventBus drop 仅 Warn 无 counter；(d) metrics 缺 outbox/event 命名空间；(e) Provider 无 GaugeVec；(f) relay pending depth 无 Gauge；(g) consumer reject 无 counter；修复: Provider 加 GaugeVec + 三套 collector 工厂 + bootstrap phase 5/6 自动 wire + 5 新 metric | feat | P1/Cx3 | 🟡 | — | `runtime/observability/metrics/{shutdown,outbox,event}.go` + `runtime/bootstrap/` + `kernel/observability/` | 030 §2 R-01 + G-05 | cap-13 |
| R-02 | **EVENTBUS-DROP-CONTEXTUAL-LOG** — InMemoryEventBus.broadcast/roundRobin drop 路径 slog.Warn 缺 entry_id/aggregate_id/event_type；修复: 升 Error 级 + 三字段（与 R-01 counter 对应）| bug | P2/Cx1 | 🟡 | — | `runtime/eventbus/eventbus.go` | 030 §2 R-02 | cap-08 |
| R-03 | **BOOTSTRAP-NIL-OPTION-CONSISTENCY** — `WithManagedCloser(nil)` 静默接受，`WithManagedResource(nil)` phase0 fail-fast — 相邻 API 风格冲突；修复: 两者均改 fail-fast | bug | P2/Cx1 | 🟡 | — | `runtime/bootstrap/options.go` | 030 §2 R-03 | cap-12 |
| A-01 | **OIDC-FAILFAST-MR-COMPLETENESS**（含 A-07/A-08）— (1) `oidc.New(ctx, cfg)` 同步 discover；(2) 4 adapter (postgres/redis/s3/oidc) 实现 `Checkers()` 返回 `{name}_ready`；(3) s3 状态机 + 后台 health-check goroutine，probe 只读最新结果；(4) archtest `MANAGED-RESOURCE-COMPLETENESS-01`；(5) postgres.Pool 升 ManagedResource(Checkers + Worker=nil)；(6) `adapters/adapterutil/` Health → Checkers helper 下沉 4 adapter 复用 | feat | P0/Cx3 | 🔴 | — | `adapters/{oidc,postgres,redis,s3}/` + `adapters/adapterutil/` (新) + `tools/archtest/` | 030 §2 A-01 + A-07 + A-08 | cap-13 |
| C-02 | **CONFIGSUBSCRIBE-CACHE-LIFECYCLE** — configsubscribe.Cache 进程内无界 + 未挂 Lifecycle，长寿进程内存增长；修复: 挂 `kernel/cell.LifecycleHook` OnStart hydrate / OnStop snapshot；改 LRU + size cap；暴露 `eventbus_cache_size` metric | bug | P1/Cx2 | 🟡 | — | `cells/configcore/slices/configsubscribe/` | 030 §2 C-02 | cap-09 |
| C-04 | **CELLS-INIT-TEMPLATE-CONVERGE**（含 C-07 emitter health probe helper）— 3 cell Init 切分各异 + internal/ 子包不对称；修复: `kernel/cell` 提供 `BaseCell.RegisterStandard(reg, StandardInit{...})` 模板 + scaffold 预生成 `internal/{ports,domain,dto,events,mem}` 五目录 + 3 cell 改造 + scaffold 升级 + 抽 `cell.RegisterEmitterHealthProbes(reg, emitter)` helper（删 3 cell 4 处重复）| refactor | P2/Cx2 | 🟡 | K-06 落地后 | `kernel/cell/` + `cells/{accesscore,auditcore,configcore}/` + scaffold 模板 | 030 §3 C-04 + C-07 | cap-01 |
| C-05 | **CELLS-CELLROUTES-PLACEHOLDER-DELETE** — `configcore/cell_routes.go` 退化为占位（仅注释）；修复: 直接删除文件；迁移上下文挪到 commit message | refactor | P2/Cx1 | 🟡 | — | `cells/configcore/cell_routes.go` | 030 §3 C-05 | cap-09 |
| C-06 | **L0-CELL-DECISION** — `l0Dependencies: []` 在 3 cell 全空，无任何 `type: l0` 实例，schema 字段是死代码路径；修复: 二选一 (a) 升 `pkg/query.CursorCodec` 等共享逻辑为示例 L0 cell；(b) 文档明确"L0 cell 是未来扩展点，当前无实例" | doc | P2/Cx1 | 🟡 | — | `cells/` + `kernel/metadata/` + docs | 030 §3 C-06 | cap-01 |
| C-09 | **CELL-SPLIT-LAYOUT-NORMALIZE** — accesscore + configcore 三文件范式不一致：(a) `configDirectPublishMode`/`ensureCursorCodec` 是 pure helper 但放 `cell_init.go`；(b) `RegisterSubscriptions` 放 `cell_routes.go` 名不副实；修复: 引入 `cell_lifecycle.go`（订阅注册）+ `cell_helpers.go`（pure helper）命名惯例；反向迁移 + scaffold 模板同步 | refactor | P2/Cx2 | 🟡 | K-07 一并 | `cells/accesscore/` + `cells/configcore/` + scaffold | 030 §3 C-09 | cap-01 |
| G-07 | **OUTBOX-WRITER-MUST-CONTRACT** — (a) `Writer.Write` 注释 SHOULD 而非 MUST 参与事务；(b) outbox/command 中 `MaxMetadataKeys` 等校验完全重复；(c) `HandleResult.Receipt` exported 但禁止 handler 读写；(d) 缺 `Ack()/Requeue(err)/Reject(err)` 工厂；修复: 改 MUST + `TxRunner.RunInTx` godoc 强制 + 提取 `kernel/metautil` + Receipt 改 unexported + 提供工厂函数 | arch-opt | P1/Cx2 | 🟡 | — | `kernel/outbox/` + `kernel/command/` + `kernel/metautil/` (新) | 030 §3 G-07 | cap-07 |
| G-08 | **OUTBOX-FAILOPEN-COUNTER + INMEM-RECEIPT-FIX** — (a) fail-open `RecordDrop()` 无 metrics；(b) `inMemReceipt.Commit/Release` 共享 `sync.Once`，Release 先于 Commit 静默 false-success；(c) `UnmarshalEnvelope` `msg.ID` 仅非空检查，可日志注入（CWE-117）；修复: increment `outbox_failopen_drops_total{cell}` + `committed atomic.Bool` 区分 + 复用 `idutil.IsSafeID` | bug | P1/Cx2 | 🟡 | — | `kernel/outbox/` + `runtime/outbox/` + `pkg/idutil/` | 030 §3 G-08 | cap-08 |
| G-10 | **KERNEL-CELL-PACKAGE-DECOMPOSE** — kernel/cell 是 god-package：含 AuthPlan(JWT/MTLS) + Outbox EmitterFactory + Health alias；`Cell` 接口 11 方法混合生命周期与元数据自省；3 个 "registry" 命名混乱；修复: (1) `auth_plan.go` → `kernel/auth/`；(2) `mode_resolver.go` → `kernel/outbox/` + 改名 `emitter_resolver.go`；(3) `cell.Registry` → `cell.Registrar`；(4) `Cell` 拆 `CellLifecycle` + `CellDescriptor`；删 `health.go` 单行 alias | refactor | P1/Cx3 | 🟡 | 与 029 #13 PR-A22 协同 | `kernel/cell/` + `kernel/auth/` + `kernel/outbox/` + `kernel/registry/` | 030 §3 G-10 | cap-01 |
| G-11 | **SCAFFOLD-FREETEXT-YAML-INJECTION** — `Goal`/`OwnerTeam` 自由文本写 YAML 无字符过滤，`\n` 注入产生额外键绕过 VERIFY/FMT；修复: `validateFreeText()` 拒 `\n\r":#[]{}` + 模板裸 scalar 改单引号包裹 + `TestCreateJourney_YAMLInjection` 对抗测试 | bug | P1/Cx2 | 🟡 | 发布前安全收口 | `kernel/scaffold/` + `kernel/governance/` | 030 §3 G-11 | cap-02 |
| G-12 | **CRYPTO-INTERFACE-HARDENING** — (a) `MatchKeyID` 普通字符串比较，时序侧信道；(b) `KeyHandle.Encrypt` MUST nonce 唯一无 contract test；(c) `KeyHandle.Encrypt` vs `ValueTransformer.Encrypt` 返回值顺序漂移；修复: `crypto/subtle.ConstantTimeCompare` + `TestKeyHandle_NonceUniqueness` contract test + `EncryptResult { Ciphertext, Nonce, EDK []byte; KeyID string }` 统一签名 | bug | P1/Cx2 | 🟡 | 安全审查触发 | `kernel/crypto/` | 030 §3 G-12 | cap-10 |
| G-13 | **GOVERNANCE-RULES-REGISTRATION-GUARD** — (a) `Validator.rules()` 手工 slice，漏注册零反馈；(b) `ValidateStrict`/`ValidateStrictFailFast` 双列表漂移；(c) error 规则无修复指导；(d) rule code 字面量散落；修复: archtest 反射枚举 + 统一 `ValidateStrict(strict, failFast bool)` 单入口 + error 规则参照 ADV-06 追加 `; fix: ...` + 提取 `rulecodes.go` | arch-opt | P1/Cx2 | 🟡 | — | `kernel/governance/` + `tools/archtest/` | 030 §3 G-13 | cap-02 |
| G-14 | **VERIFY-PRINTER-ZEROMATCH-WARN** — text printer 对 `TestResult.ZeroMatch=true` 无警告，与 `[PASS]` + 实际跑 N 个测试输出完全相同；修复: `printTestResults` 检测 `tr.ZeroMatch` 输出 `[WARN] %s — no tests matched -run pattern` | bug | P1/Cx1 | 🟡 | — | `cmd/gocell/app/printers/verify.go` | 030 §3 G-14 | cap-14 |
| G-15 | **KERNEL-METADATA-CODEGEN-OVERLAY** — kernel/metadata 既是被动数据又承载 `goStructName` 等 codegen-only 字段；修复: 二选一 (a) 把 codegen-only 字段挪到 `tools/codegen` schema overlay；(b) `metadata/doc.go` 注明"YAML schema 总账本"故意承载多消费方所需字段 | refactor | P2/Cx2 | 🟡 | — | `kernel/metadata/` + `tools/codegen/` | 030 §3 G-15 | cap-02 |
| J-02 | **JOURNEYS-FIXTURES-DECISION** — `fixtures/` 仅 `.gitkeep`，CLAUDE.md 声明"供 run-journey 使用"但 schema 缺 fixtures 字段；修复: 二选一 (a) 删除 `fixtures/` + 撤回 CLAUDE.md 引用；(b) 引入 `fixtures: [fixture-id]` 字段 + runner 注入机制 | doc | P1/Cx1 | 🟡 | — | `fixtures/` + `kernel/metadata/` + CLAUDE.md | 030 §3 J-02 | cap-14 |
| J-03 | **CONTRACT-V1V2-DRY-RUN** — api-versioning.md 写 v2 规则但 0 实例、0 deprecation 模板、无 v1/v2 共存示例；修复: 选 contract（如 audit list）走一遍 v1→v2 演练（目录 + ContractMeta.id + ownerCell 双挂 + lifecycle + outbox triggers + journey checkRef）+ ADR；或写 ADR 明确"1.0 之前不做 v2 升级"删 v2 段落 | feat | P1/Cx2 | 🟡 | v1.1 启动 | `contracts/` + `.claude/rules/gocell/api-versioning.md` + ADR | 030 §3 J-03 | cap-02（v2 deprecation 与 029 F4 同根，可合并） |
| J-04 | **CONTRACT-SCHEMA-NAMING-NORMALIZE** — (a) api-versioning.md 写 `pageSize`，contract 实际用 `limit`（规则与代码漂移）；(b) event headers `event_id`(snake_case) 与 cell-patterns.md "camelCase" 冲突；修复: 改规则文档 + 与 J-03 v1→v2 演练搭车统一 envelope | bug | P1/Cx1 | 🟡 | 与 J-03 同 PR | `.claude/rules/gocell/` + `contracts/` | 030 §3 J-04 | cap-04 |
| F-01 | **CODEOWNERS-PR-TEMPLATE** — 缺 `.github/CODEOWNERS` + `pull_request_template.md`，reviewer 路由全靠手动，PR 描述无强制 ref/一致性级别/journey 影响面；修复: 新建 CODEOWNERS（`/kernel/ @owner-kernel` 等）+ pull_request_template.md（4 项 checklist）+ branch protection 配置文件 | doc | P1/Cx1 | 🟡 | — | `.github/CODEOWNERS` (新) + `.github/pull_request_template.md` (新) | 030 §3 F-01 | cap-x-cross |
| F-02 | **MAKEFILE-LINT-RACE-ARCHTEST** — Makefile 13 target 缺 `lint`/`race`/`archtest` 独立 target，CI 与本地命令漂移，lint exclusions 13 条无周期复盘；修复: `make lint`/`race`/`archtest` + CI yaml 改调 Makefile + `hack/verify-lint-exclusions.sh` 校验时间戳 | feat | P1/Cx2 | 🟡 | — | `Makefile` + CI workflows + `hack/` | 030 §3 F-02 | cap-x-cross |
| F-03 | **PKG-CONTRACTS-BOUNDARY-DOC + ARCHTEST** — `pkg/contracts` 角色未在 README/doc.go 说明，未来若放业务领域类型 archtest 不会立即报；`pkg/ctxkeys` 与 `kernel/ctxkeys` 边界微妙；修复: `pkg/contracts/doc.go` 明确"仅承载 contracts/*.yaml Go 类型镜像 + Schema helper" + archtest `PKG-CONTRACTS-NO-BUSINESS-TYPE` + `PKG-CTXKEYS-NO-CELL-MODEL` | doc | P1/Cx2 | 🟡 | — | `pkg/contracts/doc.go` (新) + `tools/archtest/` | 030 §3 F-03 | cap-03 |
| F-04 | **CMD-GOCELL-VS-COREBUNDLE-DOC** — cmd/CLAUDE.md 主题是 corebundle 三层组装，对 cmd/gocell 在 Composition Root 中地位完全没着墨；修复: 文首加对照段：cmd/gocell = 治理/元数据/生成器 CLI（dev+CI）；cmd/corebundle = assemblies/corebundle/ 运行时组装产物 | doc | P2/Cx1 | 🟡 | — | `cmd/CLAUDE.md` | 030 §3 F-04 | cap-x-cross |
| F-05 | **QODANA-WORKFLOW-AUDIT** — Qodana 与 CodeQL/Semgrep 双重覆盖、增量价值未在 yaml 注释说明；`pr-mode: false` 不阻断 PR；修复: 二选一 (a) 补 yaml 头部注释明确差异化覆盖；(b) retire workflow + 删 `QODANA_TOKEN` secret | doc | P2/Cx1 | 🟡 | — | `.github/workflows/qodana_code_quality.yml` | 030 §3 F-05 | cap-x-cross |
| F-06 | **REQUIREMENTS-TRACEABILITY-CHAIN** — 无 `docs/requirements/` 目录；ADR/Roadmap/journey goal 三处隐含需求；contract.yaml/journey 无 `requirementID` 反向链；V 模型左侧追溯断点；修复: 引入 `docs/requirements/REQ-*.yaml` (id/text/category/priority/satisfiedBy/verify) + contract.yaml + journey schema 加 `requirementID: []` + archtest `REQ-TRACE-01` 双向校验 + 1-2 ADR | feat | P2/Cx3 | 🟡 | V 模型左侧补全启动 | `docs/requirements/` (新) + `kernel/metadata/` + `tools/archtest/` + ADR | 030 §3 F-06 | cap-x-cross |
| F-07 | **SYSML-VIEW-CODEGEN** — 5 张 SysML 图（BDD/IBD/用例/活动/状态机）有元数据天然映射但无生成器；修复: 新建 `tools/sysmlgen/` → `generated/sysml/<view>.{puml,mermaid}` + CI step `make sysml-verify` | feat | P3/Cx3 | 🟡 | F-06 落地后 | `tools/sysmlgen/` (新) + `generated/sysml/` (新) | 030 §3 F-07 | cap-14 |
| F-09 | **CONSTRAINTS-PARAMETRIC-FIELD** — cell.yaml 无 `constraints` 字段；SLO/性能/容量约束写在 PR 描述而非模型；修复: 加 `constraints: { latency: {p99_ms, p999_ms}, throughput: {publish_per_second}, capacity: {queue_depth_max} }` + verify 钩子跑 micro-benchmark 校验 | feat | P3/Cx3 | 🟡 | F-06 落地后 | `kernel/metadata/types.go` + `kernel/verify/` + cell.yaml schema | 030 §3 F-09 | cap-02 |

---

## 历史与参考

- 原 backlog 305 行已备份到 [`docs/backlog/archive/backlog.md`](backlog/archive/backlog.md)（develop @ 18a06ab7 快照），含被本次迁移**跳过**的 narrative 段：
  - `## 架构演进里程碑（M0-M4，源自 ADR-202605041430）` — **M0 已大部分完成**（poolstats 接口下沉 PR#387 / Noop archtest / CellMeta 合一）；**M1/M2/M3/M4 已提取为 4 条 backlog item**（M1→cap-13、M2→cap-02、M3→cap-02、M4→cap-14）；narrative 段保留在 archive 作为完整 ADR 上下文
  - `## 设计决策记录（历史 — 不修，避免重复审查）`
  - `## v1.1+ 长期规划`
  - `## 工时汇总`
- `docs/backlog1.md` (231 行，2026-04-26 草案) / `docs/backlog2.md` (431 行，2026-04-29 4-archive) / `docs/backlog_later_detail.md` (91 行，V1.1+ 详解) / `docs/tech-debt-registry.md` (224 行，跨 Phase 技术债) 已分别并入本文件，原档完整备份到 [`docs/backlog/archive/`](backlog/archive/) 同名文件，原路径改成 1 段重定向桩。
- 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
