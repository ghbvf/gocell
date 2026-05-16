# ConfigCore 系列残余项修复计划

**生成日期**: 2026-05-10
**基线**: `develop @ b30afc52`（PR #440 合入后）
**来源**: 用户口头列出 11 项 backlog；逐项核实代码现状（见 `docs/backlog.md` + 以下条目对应 source）
**用途**: 把 ConfigCore 域 + 跨 cell 受影响的 11 项收口为 6 个 PR，给出每项的最新修复方向、文件域、估时与依赖。**不重写 backlog 详情**，只把执行口径补齐。

> 标记说明：
> 🟢 已纳入计划/已合并 = 不再独立维护
> 🟡 可延后 = 不卡正确性或安全；技术债 / 测试覆盖 / 信号待发
> 🟠 条件延后 = 有明确触发条件
> 🔴 = 发布前必做硬约束

---

## 0. 代码现状核实

| # | 条目 | 状态 | 关键证据 |
|---|------|------|---------|
| 1 | CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 | ✅ 已解决（PR 207-cfg-cache-lifecycle） | Cache 确认 service-private；OWNER-01 收为 won't-do（见 §2 + §5） |
| 2 | C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE | ✅ 已解决（PR 207-cfg-cache-lifecycle） | tombstone TTL GC + `eventbus_cache_tombstone_evicted_total` counter + `EventbusCacheCollector` wired into configcore |
| 3 | B2-C-11 Configsubscribe tombstone 无 TTL | ✅ 已解决（PR 207-cfg-cache-lifecycle） | `WithTombstoneTTL` Option（24h default in configsubscribe.NewService）+ GC goroutine + eviction metric |
| 4 | CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | ⚠️ 撤回主方案 | `cells/accesscore/slices/configreceive/service.go` 头部注释自承「Real consumers (JWT TTL refresh, key rotation interval) will land in a follow-up」——是为业务 reload 已搭好的接入骨架，单纯删除会返工 ~10h；改为业务触发型，参 §6 与 backlog `CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01` |
| 5 | PR-CFG-A-DEFER-2 (L2 divergence) | ⚠️ 未实施 | 已有完整设计草案（参 `docs/bak/202605010354-backlog-pre-pr341-cleanup.md` L57） |
| 6 | C-05 CELLS-CELLROUTES-PLACEHOLDER-DELETE | ✅ 已合（PR-CFG-CELL-ROUTES-CLEAN） | `cells/configcore/cell_routes.go` + `cells/accesscore/cell_routes.go` 同源 4 行占位均删除 |
| 7 | PR320-FU-CONFIGCORE-CI-NOOP | ✅ 已完成（代码端态核对 develop@67f5ce917）| `configpublish/service_test.go:37-43` godoc 明确 newTestService 用 NoopEmitter 覆盖 noop publisher CI 路径；fail-open WARN 由 `Test{Service_Publish,Service_Rollback}_FailOpen_PublisherError` 单独断言 |
| 8 | PR-CFG-G1-FU6 | ✅ subsumed by FMT-21 (PR-CFG-G1-FU6-RECYCLE) | `kernel/governance/rules_misc_strict.go:551-602` (`validateFMTContractDirIDMatch01`) is the bijective inverse and already fires on the canonical regression. Regression test pinned at `rules_misc_strict_test.go::TestFMTContractDirIDMatch01_Mismatch` "dash-instead-of-slash regression"; docstring aliased. |
| 9 | PR238-FU4 legacy NotFound 测试去重 | ✅ 已完成（代码端态核对 develop@67f5ce917）| legacy 命名函数已删；`config_repo_test.go` NotFound 与 OtherScanError 已分离（`TestGetByKey_NotFound_*`/`TestGetByKey_OtherScanError_*`、`TestGetVersion_*` 各一对），无旧重复对 |
| 10 | PR238-FU8 UpdateForRollback op label 断言 | ✅ 已完成（代码端态核对 develop@67f5ce917）| `config_repo_test.go:408 assert.Contains(ec.InternalMessage, opUpdateForRollback)` + `:366 assert.NotContains`（Update 路径反向断言），与 fix spec 完全一致 |
| 11 | CELLS-SLICE-MULTI-VERB-DECOMPOSE-01 | 🟡 部分完成（代码端态核对 develop@67f5ce917）| **auditappend ✅ 已 4 拆**（`auditappend{config,role,session,user}`，旧 `auditappend` slice 已删）——但**偏离设计**：各子 slice 独立 `service.go`，**未走 plan 要求的 `internal/dispatch.go` 共享 helper**（全仓无 dispatch.go）。**configread ❌ 未拆**：`configread/slice.yaml:8` 仍 `http.config.internal.get.v1` 与 public serve 同 slice，无 `configread-internal` slice → SLICE-DECOMP 第(2)项未达成 |

**汇总**（2026-05-16 代码端态回灌 develop@67f5ce917）：已解决 8（#1/#2/#3 PR 207-cfg-cache-lifecycle；#6；#7/#9/#10 测试硬化已落；#8 subsumed by FMT-21）/ 撤回主方案 1（#4，改业务触发）/ 部分完成 1（#11 auditappend 4 拆毕但偏离 dispatch 设计、configread 未拆）/ 实际未实施 1（#5 PR-CFG-L2-DIVERGENCE）

---

## 1. PR 分组矩阵

| PR | 主题 | 条目 | Cx 上限 | 估时 | 触发/依赖 |
|----|------|------|---------|------|-----------|
| **PR-CFG-CACHE-LIFECYCLE** | configsubscribe 缓存生命周期统一治理 | #1, #2, #3 | Cx2 | 1-1.5d | 无 | ✅ shipped **PR #518**（含深度 review F1–F3：TTL clamp-up + GC 状态机）|
| **PR-CFG-TEST-RESIDUALS** | configcore 测试补丁批 | #7, #9, #10 | Cx1 | 0.5d | ✅ 已完成（#7/#9/#10 代码端态核对落实，develop@67f5ce917）|
| ~~**PR-CFG-PLACEHOLDER-CLEAN**~~ → **PR-CFG-CELL-ROUTES-CLEAN** | configcore + accesscore cell_routes.go 占位清理 | ~~#4,~~ #6 | Cx1 | 0.1d | 无（已合）|
| **PR-CFG-L2-DIVERGENCE** | ConfigCore L2 与 memory 行为分歧治理 | #5 | Cx1（决策）+ Cx2（实施） | 1d 设计 + 4h 实施 | architect 评估 |
| **PR-CFG-G1-FU6-RECYCLE** | ~~CONTRACT-PATH-ID-MAPPING-ARCHTEST~~ → **subsumed by FMT-21**; pin regression test + alias docstring | #8 | Cx1 | 0.5h | subsumed-by: FMT-21 (`validateFMTContractDirIDMatch01`) |
| **PR-CFG-SLICE-DECOMPOSE** | auditappend / configread 多 verb 拆分 | #11 | Cx3 | 1.5-2d | 🟡 部分：auditappend 4 拆 ✅（偏离——无 internal/dispatch.go，各 slice 独立 service.go）；configread→configread-internal ❌ 未拆，仍需推进 |

---

## 2. 详细修复表

### PR-CFG-CACHE-LIFECYCLE — configsubscribe 缓存生命周期统一治理

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| C-02 | **CONFIGSUBSCRIBE-CACHE-LIFECYCLE-01** (Cx2, P1, 🟡): `configsubscribe.Cache` 进程内 map 无界 + 未挂 Lifecycle，长寿进程内存增长。**修复**：(1) Cache 实现挂 `kernel/cell.LifecycleHook`，OnStart hydrate / OnStop snapshot；(2) 改 LRU + 容量 cap（cap 由 cell.yaml `params.cacheMaxEntries` 注入，缺省 10000）；(3) 暴露 `eventbus_cache_size{slice="configsubscribe",cell="configcore"}` gauge。**对标**：Watermill `MessageMemoryCache` LRU + capacity；K8s `client-go/tools/cache.Store` 显式 lifecycle。 | 5h | `cells/configcore/slices/configsubscribe/service.go` + `cells/configcore/cell_init.go` + 新 `runtime/observability/metrics/eventbus_cache.go` | 030 §2 C-02 |
| OWNER | **CONFIGCORE-CACHE-LIFECYCLE-OWNER-01** — **won't-do（2026-05-16）**。实施阶段确认 Cache 仅由 `configsubscribe/service.go` 及其单元测试引用，无跨 slice 共享，满足本条自身决策准则（"如发现 Cache 仅由 service 私有，本条收为 won't-do"）。未引入 archtest 抽象。详见 §5。 | — | — | PR 207-cfg-cache-lifecycle |
| B2-C-11 | **CONFIGSUBSCRIBE-TOMBSTONE-TTL-01** (Cx2, P2, 🟡): `service.go:29,169` tombstone 永久保留导致内存膨胀。**修复**：tombstone 增加 `expiresAt` 字段，TTL 从 `params.tombstoneTTL` 注入（缺省 24h）；OnStart 启动定期清理 goroutine（tick 间隔 1/10 TTL，跟 LifecycleHook OnStop 联动 stop）；evict 触发 metric `eventbus_cache_tombstone_evicted_total` counter 自增。**对标**：Cassandra `gc_grace_seconds`；CockroachDB MVCC GC。 | 3h | 同 service.go + cell_init.go + eventbus_cache.go | backlog2 §4 B2-C-11 |

**PR 207-cfg-cache-lifecycle 实施范围决策（2026-05-16）**：

- D1: 未实施 OnStart hydrate / OnStop snapshot — 无持久化后端，内存 map 重启即重建，正确性由版本单调性 + 事件回放保证；对标 Watermill `MapExpiringKeyRepository` / k8s informer / go-micro 均无跨重启 cache 持久化。
- D2: TTL 通过 `configcore.WithTombstoneTTL` Option 注入（cell.yaml 无 params 模型）；composition root 注入，24h default 在 `configsubscribe.NewService` 内。**PR #518 深度 review 后更新（F1–F3）**: 原"Warn 不硬 clamp"立场被超越 — 任何低于 Claimer idempotency window 的调用方 TTL 在 `NewService` 内被 clamp-up 到 `idempotency.DefaultTTL`（硬不变量）；resurrection-via-stale-replay 路径已封闭。`defaultTombstoneTTL` 改为 `const defaultTombstoneTTL = idempotency.DefaultTTL`（单一真值源）。GC Stop/Restart 状态机引入 `gcStopping` 字段，timeout 后保留状态供后续 Stop 继续等同一 goroutine。`runTombstoneGC` 死卫与 `StartTombstoneGC tombstoneTTL<=0` 分支均已删除（clamp 后永远 >0）。
- D3: `eventbus_cache_size` gauge → 单 counter `eventbus_cache_tombstone_evicted_total`（kernel Provider 无 Gauge by design；`shutdown_metrics.go` 先例）。
- D5（自审）: 完全放弃 LRU/cap — LRU 驱逐活跃 entry 会静默破坏单调回放守护（与过早墓碑 GC 同类风险）；Watermill `MapExpiringKeyRepository` 原始码为纯 TTL 无 LRU/cap（原 §1 benchmark 引用不准确）；活跃 entry 由 live config keyspace 自然有界。内存治理 = tombstone TTL GC only。

### PR-CFG-TEST-RESIDUALS — configcore 测试补丁批

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| PR320-FU | **CONFIGCORE-NOOP-PUBLISHER-CI-COVERAGE-01** — ✅ **DONE**（代码端态核对 develop@67f5ce917：`configpublish/service_test.go:37-43` noop wiring 覆盖 + `Test*_FailOpen_PublisherError` fail-open WARN 断言）(Cx1, P3, 🟡): `cells/configcore/` noop publisher 路径在 CI 中未走通，回归会被静默吞掉。**修复**：在 `cells/configcore/slices/configpublish/service_test.go` 加一个 noop publisher 分支测试（不引入新 fixture，复用 `outboxtest.NoopWriter`），断言 publish 路径在 noop wiring 下走 fail-open WARN 而非 panic / 静默丢弃。 | 1.5h | `cells/configcore/slices/configpublish/service_test.go` | PR#320 / PR-A55 adapter integration skip gaps |
| PR238-FU4 | **CONFIGREPO-LEGACY-NOTFOUND-TEST-DEDUP-01** — ✅ **DONE**（代码端态核对 develop@67f5ce917：legacy 命名函数已删，`config_repo_test.go` NotFound 与 OtherScanError 分离，无旧重复对）(Cx1, P3, 🟡): `config_repo_test.go:314` `TestConfigRepository_GetByKey_NotFound` 与 `:665` `TestConfigRepository_GetVersion_NotFound` 用 `assert.AnError` 实际测的是 other-error 分支，与 `TestGetByKey_OtherScanError_ReturnsErrConfigRepoQuery` / `TestGetVersion_OtherScanError_ReturnsErrConfigRepoQuery` 重复，造成 mutation-test 误导。**修复**：删除两个 legacy 命名函数（直接 delete，不向后兼容）；如有 PR diff 需要可读性，把覆盖点合并到 OtherScanError 表驱动行内。 | 1h | `cells/configcore/internal/adapters/postgres/config_repo_test.go` | PR#238 L4 reviewer T-04 |
| PR238-FU8 | **CONFIGREPO-UPDATE-ROLLBACK-OP-LABEL-TEST-01** — ✅ **DONE**（代码端态核对 develop@67f5ce917：`config_repo_test.go:408 assert.Contains(InternalMessage, opUpdateForRollback)` + `:366 NotContains` 反向断言，与 fix spec 一致）(Cx1, P3, 🟡): `doUpdate` 通过 `op` 参数向 `scanConfigOrMapError` 传 `"Update"` / `"UpdateForRollback"`，`InternalMessage` 携带该 op，但 `TestConfigRepository_UpdateForRollback_NotFound:395-411` 与 `TestConfigRepository_UpdateForRollback` 都未断言 InternalMessage 含 `"UpdateForRollback"`——若有人把 op 硬编码回 `"Update"`，CI 不会 FAIL。**修复**：相关 NotFound 测试追加 `assert.Contains(t, ec.InternalMessage, "UpdateForRollback")` + `Contains(..., "Update")` 反向断言（`Update` 子串检查不区分两路径，要先取 InternalMessage 再 Contains 唯一关键词）。 | 1h | 同上 | PR#238 L4 round-2 reviewer T-R4 + 029 master roadmap §errcode W4 |

### PR-CFG-CELL-ROUTES-CLEAN — cell_routes.go 占位清理（已合）

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| C-05 | **CELLS-CELLROUTES-PLACEHOLDER-DELETE-01** (Cx1, P2, ✅ done): `cells/configcore/cell_routes.go` 与 `cells/accesscore/cell_routes.go` 同源退化为 4 行注释占位（K#05 W4 / Batch 3 migration 残留——"HTTP route group and event subscription wiring are now owned by cell_gen.go"）。**修复**：直接 `git rm` 两文件；迁移上下文挪到 commit message 与本 PR 描述。无 import 引用——`cell_gen.go` 已自动拥有 HTTP route groups 与 event subscription wiring。 | 0.5h | `cells/configcore/cell_routes.go` + `cells/accesscore/cell_routes.go` | 030 §3 C-05 |
| ~~C-01~~ | **CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01** — 撤回主方案，改业务触发。详见 §6。 | — | — | — |

### PR-CFG-L2-DIVERGENCE — ConfigCore L2 与 memory 行为分歧治理

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| PR-CFG-A-DEFER-2 | **CONFIGCORE-L2-MEMORY-MODE-DIVERGENCE-01** (Cx1 决策 + Cx2 实施, 🟡): `cells/configcore/cell.yaml` 声明 `consistencyLevel: L2`（OutboxFact），但当 `GOCELL_CONFIGCORE_STORAGE_BACKEND=memory` 时实际行为是 L0（无事务、无 outbox 写入路径，事件由 DirectEmitter fail-open 模式发射）。声明值与运行时行为分歧——治理工具与 readiness gate 都依赖 `consistencyLevel` 元数据决策。**候选方案**（待 architect 评估）：(a) 引入 `durabilityMode: durable\|memory` 字段，cell.yaml 声明"逻辑级别"，runtime 通过 `cell.Init()` 上报"实际级别"；(b) 拆 cell（`configcore-memory` / `configcore-pg`）；(c) 在 `gocell validate --strict` 加规则：声明 L2 的 cell 启动时必须装载 outbox.Writer（非 nil 非 noop）。**对标**：Kubernetes `Reliable / Eventual` annotation + admission webhook；Cassandra consistencyLevel 是请求级而非声明级。 | 1d 设计 + 4h 实施 | `cells/configcore/cell.yaml` + `cells/configcore/cell_init.go` + `kernel/governance/rules_*.go` | PR-CFG-A (PR#268) round-2 reviewer Finding #3 |

### PR-CFG-G1-FU6-RECYCLE — CONTRACT-PATH-ID-MAPPING archtest

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| PR-CFG-G1-FU6 | **RECYCLED → subsumed by FMT-21** (Cx1, P2, ✅ done): `kernel/governance/rules_misc_strict.go:551-602` 的 `validateFMTContractDirIDMatch01`（FMT-21）是 path-id-mapping 的 bijective inverse——既有规则用 `id → derived dir` 方向比对 `c.Dir`，与提议的 `path → derived id` 方向比对 `c.ID` 在数学上等价（共用 `split(".") ↔ join(".")` 算法），对原 plan 描述的 `id: http.config.internal-get.v1` at `contracts/http/config/internal/get/v1/` 同样 fire。FMT-21 还覆盖更广（`examples/*/contracts/...` 通过 parts-walk）。**实际改动**：(1) 在 `TestFMTContractDirIDMatch01_Mismatch` 表中追加 dash-vs-slash 回归 case 与"合法 dash" 反向 case 各 1 条，pin FMT-21 对该规则的覆盖；(2) FMT-21 docstring alias 为 `also satisfies FMT-CONTRACT-PATH-ID-MAPPING-01` 防止再 re-file。**AI-rebust**：分两层评级——(a) **contract-author 违反**：Hard（contract author 不能 ship path↔id mismatch 而不触发 FMT-21 SeverityError，违反不可表达）；(b) **enforcement 载体**：Medium（docstring alias 是 Soft 注释锚点，但 dash-regression integration test case 通过 `v.Validate(t.Context())` 全链路守卫 FMT-21 在 `rules()` 切片中的 membership——移除 FMT-21 注册立刻破坏该 wantCount:1 case，CI 红）。 | 0.5h | `kernel/governance/rules_misc_strict.go` (docstring) + `kernel/governance/rules_misc_strict_test.go` (2 表行) | PR-CFG-G1 review 新登记（参 `docs/plans/archive/202604260058-l4-virtual-taco.md` L375）；典型受害样本：`contracts/http/config/internal/get/v1/contract.yaml` (id 写成 `http.config.internal-get.v1` 即被 FMT-21 拦) |

### PR-CFG-SLICE-DECOMPOSE — auditappend / configread 多 verb 拆分

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| SLICE-DECOMP | **CELLS-SLICE-MULTI-VERB-DECOMPOSE-01** — 🟡 **PARTIAL**（代码端态核对 develop@67f5ce917：(1) auditappend ✅ 已拆 `auditappend{config,role,session,user}` 4 子 slice、旧 slice 已删，**但偏离设计——无 `internal/dispatch.go` 共享 helper，各 slice 独立 `service.go`**；(2) configread ❌ **未拆**，`configread/slice.yaml:8` 仍 `http.config.internal.get.v1`+public serve 同 slice，无 `configread-internal`。**剩余工作 = configread→configread-internal 拆分**，dispatch helper 偏离是否回补需 review 决策）(Cx3, P1, 🟡): `cells/auditcore/slices/auditappend/slice.yaml` 14 contractUsages 单 slice 承载 13 topic（session/user/config/role 四类），违反 slice 单一 verb 原则；`cells/configcore/slices/configread/` 双 listener（public GET + internal-get）也不应同 slice。**修复**：(1) auditappend → `auditappend-{session,user,config,role}` 4 个子 slice 共享 dispatch helper（在 `internal/dispatch.go` 抽出）；(2) configread → 拆出 `configread-internal` 单独 slice（internal listener 独立 owner）；(3) 不留兼容包装（项目无外部消费方）；(4) 同步更新 contract.yaml `endpoints.subscribers`；(5) `gocell validate` 全量过。**反方观点**：拆分会增加 cell init.go 复杂度（4 子 slice register + dispatch helper）；有人会建议 fold 14 contractUsages 是 OK 的，因为 audit 本身就是「13 topic 单一处理」。**决策**：按现有 systems layer review 走拆分；review checklist 已签字。**注意**：与 PR-CFG-CACHE-LIFECYCLE 同动 `cells/configcore/slices/`，**必须在前者合并后再启**避免 merge conflict。 | 1.5-2d | `cells/auditcore/slices/auditappend/` 拆 4 + `cells/configcore/slices/configread/` 拆 2 + `cells/auditcore/slices/auditappend/internal/dispatch.go`（新） + 全部 contract.yaml endpoints.subscribers + cell_init.go | systems layer review + 030 §2 K-07 |

---

## 3. 推荐执行顺序与依赖

```
并行起点（无依赖）：
  ├─ PR-CFG-TEST-RESIDUALS    (~0.5d) — 零冲突，最先合
  ├─ PR-CFG-CELL-ROUTES-CLEAN (~0.1d) — 已合（仅 #6；#4 撤回，参 §6）
  ├─ PR-CFG-G1-FU6-RECYCLE    (~3h)   — kernel/governance 域，独立
  └─ PR-CFG-L2-DIVERGENCE     (1d 设计) — architect 评估期
                                ↓
PR-CFG-CACHE-LIFECYCLE (~1-1.5d) — 解决「内存增长信号」🟠 触发条件
                                ↓
PR-CFG-SLICE-DECOMPOSE (~1.5-2d) — 在 PR-CFG-CACHE-LIFECYCLE 后做
                                  避免 cells/configcore/slices/ 冲突
```

**累计工时**: 4-5 工作日（不含 L2 设计期）。

**风险提示**：
1. PR-CFG-CACHE-LIFECYCLE 触及 LRU + Lifecycle 改动，激进自审三层（L1/L2/L3）需对照 ADR `kernel/cell.LifecycleHook` 语义，不要顺手把 owner 这层引入新抽象——简单做法是直接把 Cache 挂 `configsubscribe.Service` 自身。
2. ~~PR-CFG-PLACEHOLDER-CLEAN 的 #4 删 configreceive~~ — 已撤回主方案（参 §6）。configreceive 是为 JWT TTL refresh / key rotation 已搭好的接入骨架，单纯删除会返工 ~10h；改为业务触发型 backlog 条目。
3. PR-CFG-L2-DIVERGENCE 是元数据治理决策，**不要在没有 architect 决议前进入实施**——三个候选方案的代价差异大（方案 b 拆 cell 是 1 周，方案 c 加 strict 规则是 0.5 天）。

---

## 4. 信息已补全说明

原核实结果中标 ⚠️「信息不足」的两项已查清：

- **#5 PR-CFG-A-DEFER-2** — 设计草案已存在于 `docs/bak/202605010354-backlog-pre-pr341-cleanup.md` L57（`CONFIGCORE-L2-MEMORY-MODE-DIVERGENCE-01`），完整候选方案 a/b/c 与对标都齐。
- **#8 PR-CFG-G1-FU6** — 具体定义在 `docs/plans/archive/202604260058-l4-virtual-taco.md` L375（`CONTRACT-PATH-ID-MAPPING-ARCHTEST-01`），原属 PR-CFG-I.X2 archtest batch 但 X2 未实施，只有 typeseval helper 已落地；本规则不依赖 typeseval（纯 YAML + 路径），可独立。

无遗留信息缺口。

---

## 5. 不立项条目

- **CONFIGCORE-CACHE-LIFECYCLE-OWNER-01** → **won't-do（2026-05-16，PR 207-cfg-cache-lifecycle）**: 实施阶段确认 Cache 仅被 `cells/configcore/slices/configsubscribe/service.go` 及其单元测试引用，无跨 slice 共享实例。满足本条原始决策准则（"如发现 Cache 仅由 service 私有，本条收为 won't-do"）。未引入 archtest 抽象。同步登记于 `docs/backlog.md` cap-01 CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 行。

---

## 6. configreceive 处置（撤回原方案）

**原方案**（§3 PR-CFG-PLACEHOLDER-CLEAN C-01 行）：删除 `cells/accesscore/slices/configreceive/` 整个 slice + 把 entry-upserted/entry-deleted contract 标 `lifecycle: draft`。

**撤回理由**（2026-05-10 激进自审 L3 ADR 一致性发现）：

1. `cells/accesscore/slices/configreceive/service.go` 头部注释自承「Real consumers (**JWT TTL refresh, key rotation interval**) will land in a follow-up; the current subscription is a placeholder per ADV-05」——并非空白占位，而是为 GoCell 配置热更新已搭好的业务接入骨架。
2. 已规划业务对应：`docs/backlog.md::L132 (X3) WM-36 SecureCookie key rotation`（P3）+ `docs/backlog.md::L373 (K-02)` 提到 `J-confighotreload` journey 引用 `event.config.entry-deleted.v1`。
3. 删除连锁含 ~8 文件 + cmd/corebundle wiring + `outbox_e2e_integration_test::L391-625` PR-CFG-G1 commit 4 HTTP 闭环守卫（accesscore.configreceive → ConfigGetter.GetEntry refetch loop）；业务接入时重做约 **10h 工作量**。
4. 当前 service.go 已实现完整 ConfigGetter refetch + auth 失败分类 + metric 收集骨架（不是空 log 桩；service.go:94-131），占位的是「收到事件后触发 JWT TTL refresh / key rotation 的业务副作用」而非代码量；返工成本明显高于消除的认知噪音。

**决策**：configreceive 保留作为业务接入骨架；不在占位清理 PR 中触碰。

**新触发条件**（同步登记到 backlog `CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01` 条目）：

- 业务侧 PR 提出 JWT TTL hot-reload / key rotation 真实需求时，把 configreceive 的 placeholder log 替换为业务 reload，同时移除「placeholder per ADV-05」注释。
- 触发时机不可预知 → 紧迫度从 P1/Cx2 下调为 P2/Cx2。
- 不在 ADV-05 治理压力下提前删除已搭好的骨架（避免反优化）。

**与 §1 矩阵对齐**：~~`PR-CFG-PLACEHOLDER-CLEAN`~~ → `PR-CFG-CELL-ROUTES-CLEAN`（仅 #6，已合）；configreceive (#4) 退出本 plan 实施范围，等业务触发。

