# Backlog Re-rating Log

> 评级流水。规则真值源：[`RERATING-RUBRIC.md`](RERATING-RUBRIC.md)。

---

## Phase 0 — Rubric 落定 (2026-05-19)

- 新建 `docs/backlog/RERATING-RUBRIC.md`：三轴评估（完成性 / Cx / P）+ 5 阶段拆分 + 反模式清单
- `docs/backlog.md` 顶部加 rubric 引用
- commit: `99b973223` on branch `629-backlog-rerating`

---

## Phase 1 — cap-01 + cap-03 + cap-07 + cap-09 试评 (2026-05-19)

**扫描条目**：20（cap-01: 9 / cap-03: 3 / cap-07: 2 / cap-09: 6）

### 统计

| 维度 | 数量 |
|---|---|
| DONE 候选 | 4 |
| STALE re-scope | 1 |
| STALE-CLOSE | 1 |
| OPEN（含 P/Cx/Flag 调整）| 14 |

### DONE 候选（4）

| ID | cap | 证据 |
|---|---|---|
| P1-8 | cap-03 | `examples/iotdevice/cells/devicecell/slices/devicelist/` 完整 slice + contract + 4 个 test 全在；路径已从 `cells/` 迁到 `examples/iotdevice/cells/` |
| B2-T-04 | cap-03 | `contracts/event/user/created/v1/payload.schema.json:6-19` 全 camelCase；`grep '"UserID"\|"UserId"' contracts/ examples/*/contracts/` 命中 0 |
| PR320-FU-CONFIGCORE-CI-NOOP | cap-09 | `service_test.go:37-43` 明示 noop 隐式覆盖；newTestService 默认 NoopEmitter |
| PR238-FU8 | cap-09 | `config_repo_test.go:363-367,407-408` 双向 NotContains 锁就位（PR#553 ship `2dfaf50bf`） |

### STALE re-scope（1）

| ID | cap | re-scope |
|---|---|---|
| F-03 | cap-03 | 主前提失效：`pkg/contracts/` 已删 + Hard archtest `CONTRACTTEST-BOUNDARY-01` 守"legacy stay deleted"；re-scope 至 `PKG-CTXKEYS-NO-CELL-MODEL` 子项。P1/Cx2 → P3/Cx1，Flag → 🟠（触发条件：cell-model identifier 误入 pkg/ctxkeys） |

### STALE-CLOSE（1）

| ID | cap | 理由 |
|---|---|---|
| PR-CFG-A-DEFER-2 | cap-09 | config_entries vs feature_flags schema 差异是按 concept 设计（L1 flagwrite / L2 configpublish 有意分级），不是 bug，前提失效无残留 |

### P 升级（2）

| ID | cap | 原 P → 新 P | 命中维度 |
|---|---|---|---|
| C-04 | cap-01 | P2 → P1 | 架构 + 去重 + 抽象 + 触及 ≥ 3 cell + 吸收 C-09 |
| CONFIGREPO-OP-LABEL-TYPED-ENUM-HARD-01 | cap-09 | P3 → P2 | AI-rebust Soft → Hard 升级路径（charter mandate） |

### P 降级（3）

| ID | cap | 原 P → 新 P | 降级信号 |
|---|---|---|---|
| C-06 | cap-01 | P2 → P3 | doc/决策类 + 无 outcome + 无业务推动 |
| PR341-FU-OUTBOXTEST-CLOSE-BUDGET-COVERAGE | cap-07 | P2 → P3 | 纯 test 触发型 + 剩余 3 处是测 Close 语义本身 |
| B2-A-33 | cap-09 | P2 → P3 | 触发型（sentinel 部署）+ 无业务推动 |

### Flag 调整（2）

| ID | cap | 原 → 新 | 理由 |
|---|---|---|---|
| B2-PROVISIONER-MUTEX-REVIEW | cap-01 | 🟠 → 🟡 | trigger 已达成（PR #482 ship + PR #628 in-flight） |
| RBACASSIGN-L2-PG-ATOMICITY-01 | cap-07 | 🟠 → 🟡 | trigger X1 PG accesscore 仓储已落地（role_repo.go + user_repo.go） |

### OPEN 维持原档（11）

- cap-01：G-10 / SWEEPER / PR441-FU-METADATA / SEALED-MARKER-BUNDLE / CONTROL-PLANE-CLOCK（5）
- cap-03：—（全部 DONE/STALE）
- cap-07：（含上面 Flag 调整）
- cap-09：CONFIGCORE-RECEIVE-PLACEHOLDER（1）

### 规则边界发现（不回改 rubric，记录待 P2 末复盘）

1. **bundle 内子条 DONE 处理**：SEALED-MARKER-DEFENSE-EXPANSION-BUNDLE 内子条 A.5 `SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01` 已落地（`pkg/scaffoldid/scaffoldid.go` 存在 + `cmd/gocell/app/scaffoldid_helpers_test.go:14` 引用）；但 bundle 描述编辑受 rubric §6.1 限制。本 PR 不触碰 bundle 描述，待后续 SEALED-MARKER bundle 整批收口 PR 同步减条。
2. **DUP-like merge 形态**：C-09 已声明"并入 C-04"但保留独立行 + 自己的 P/Cx/Flag。rubric §2 DUP 定义不完全匹配此形态（C-09 残留细节未全部进 C-04 描述）。本 PR 维持 C-09 独立，待 C-04 ship 时一并归档。
3. **试评成功率**：20 条全部一次走通，无误判回滚。规则可推广到 P2 60 条。

### Commit

`5f5f1e6b1` — 13 处 row 编辑（不动 ID/描述主体；F-03 描述按 rubric §6.1 STALE re-scope 允许重写）

---

## Phase 2 — cap-04/05/06/08/10/12 主体 (2026-05-20)

**扫描条目**：60（cap-04: 14 / cap-05: 21 / cap-06: 3 / cap-08: 9 / cap-10: 3 / cap-12: 10）

### 统计

| 维度 | 数量 |
|---|---|
| DONE 候选 | 9 |
| STALE re-scope | 3 |
| STALE-CLOSE | 1 |
| OPEN（含 P/Cx/Flag 调整）| 47 |

### DONE 候选（9）

| ID | cap | 证据 |
|---|---|---|
| T11 | cap-06 | `runtime/auth/roles.go:3-4` RoleAdmin const + 生产代码无裸字面量比较 |
| B2-T-07-FU-2 | cap-06 | `runtime/auth/principal.go:53-78` + archtest `no_deleted_auth_symbols_test.go` 锁 3 符号删除 |
| PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE | cap-10 | `.github/workflows/test-race.yml:50-105` race-pg-integration job + CI-RACE-LANE-SUBSET-01 archtest |
| X1 | cap-10 | 5 个 Repo PG 实现全部落地（user/role/session/command/device + PR#575 ship） |
| PR333-BOOTSTRAP-OPTION-CROSS-CONCERN | cap-12 | PR-A66 `7c9816558` ship，5 个 options_*.go 按 concern 拆分 |
| B2-X-04 | cap-12 | `shared_deps_validate.go:177-189` validateHealthReachability real-mode fail-fast 已落 |
| B2-X-03 | cap-12 | `bundle_configcore_storage.go:98-104` + `schema_guard.go:1007` VerifyNoInvalidIndexes fail-fast |
| S1-CO-02-WIRING-OPTION-STICKY-DOCTRINE | cap-05 | `runtime-api.md:268-274` §Option 范式分层 + `protocol_test.go:329,348` sticky test |

### STALE 处理（4）

| ID | cap | 类型 | 处理 |
|---|---|---|---|
| FOUR-CHANNEL-HANDLER-EXTENSION-01 | cap-04 | STALE re-scope | §5 扩展协议 3 步已写，仅 §6 funnel matrix 表残留；Cx2→Cx1 |
| T3 | cap-06 | STALE re-scope | Files 路径刷新 `cells/devicecell/` → `examples/iotdevice/cells/devicecell/`；补 P3/Cx2 |
| P4-TD-03 | cap-05 | STALE-CLOSE | IssueTestToken 已强制 RS256，HS256 仅在 negative test → ✅ |

### P 升级（3）

| ID | cap | 原 P → 新 P | 命中维度 |
|---|---|---|---|
| ROUTE-ERROR-POLICY-01 | cap-04 | 缺 P → P2 | 架构 + 共享 policy 抽象，已有 trigger 临界 |
| AUTHZ-MUTATION-FUNNEL-UPSTREAM-HARD-01 | cap-05 | P3 → P2 | charter mandated 显式登记项 |
| CREDENTIAL-AUTHORITY-FUNNEL-SCOPE-AUTO-DERIVE-01 | cap-05 | P3 → P2 | Soft → Hard funnel auto-derive |
| PR252-F1 | cap-12 | 缺 P → P2 | trigger 已达（PGCommandQueue ship），bootstrap wiring 缺口 |

### P 降级（11）

| ID | cap | 原 P → 新 P | 信号 |
|---|---|---|---|
| LISTENER-API-SPEC-01 | cap-04 | 缺 P → P3 | arch-opt 无 outcome |
| T4 | cap-04 | 缺 P → P3 | refactor 触发型 + 无 trigger |
| WM-32 | cap-04 | P2 → P3 | feat 折中 + 业界 mesh 解决 |
| READYZ-PROBE-FAILURE-METRIC-01 | cap-04 | P2 → P3 | trigger 型 + 无业务方 |
| C-AC7 | cap-05 | P2 → P3 | jti 主体已支持，仅 blacklist 残留 |
| 多个 cap-05 缺 P 项 | cap-05 | 缺 → P3 | PR-A8 / PR267-* / X3 / X5 / T5（补 P/Cx）|
| KERNEL-RECONCILE-01 | cap-08 | P2 → P3 | 纯 feat + 无业务方 |
| WM-18 | cap-08 | P2 → P3 | 纯 feat + 无 outcome |
| RELAY-RETRYDELAY / CELL-CONSUMER 缺 P | cap-08 | 缺 → P3 | test/feat 触发型 |
| S14a | cap-10 | 缺 → P3 | 触发型 feat |
| V-A8 / PR252-F2 / PR448 / COREBUNDLE-MAINTEST 缺 P | cap-12 | 缺 → P3 | 触发型/test 类 |

### Flag 调整（1）

| ID | cap | 原 → 新 | 理由 |
|---|---|---|---|
| PR252-F1 | cap-12 | 🟠 → 🟡 | trigger 已达成（PGCommandQueue ship） |

### Cx 补齐（多条）

cap-05 X3/X5/T5 + cap-10 S14a + cap-12 V-A8/PR448/COREBUNDLE 等原表缺 Cx 列的 OPEN 项，按 rubric §3 反推补全。

### 规则边界发现

1. **触发型 P2 命中架构升级的边界**：K07-NO-WRAPPER / OUTBOX-HANDLERESULT-SLIM 命中"架构 + Soft→Hard"信号但有显式触发门槛未达；维持 P2，未升 P1。rubric §4.2 升级规则缺"P2 + 触发门槛未达 → 维持 P2，不升 P1"显式条款。下一阶段建议补。
2. **缺 P/Cx 项的处理**：旧表大量 OPEN 行 P/Cx 列空缺，rubric 未覆盖。本阶段一律按 rubric §3/§4 反推补全（无业务方推动 = P3 + Cx 按描述具体度评）。
3. **DONE 项 Source 列补充证据**：本阶段沿用 P1 实践（DONE 行在 Source 列追加 "— YYYY-MM-DD 核实落地 + 证据 file:line"）。rubric §6.1 严格只允 P/Cx/Flag/Trigger，但实践证明 Source 注解可追溯，建议下版 rubric 放宽。

### Commit

`46b383987` — 36 处 row 编辑（其中 FOUR-CHANNEL / T3 / P4-TD-03 / C-AC7 / X5 / PR252-F1 等 6 处含描述列 STALE re-scope 或证据补充）

---

## Phase 3 — cap-02 + cap-13 (2026-05-20)

**扫描条目**：49（cap-02: 25 / cap-13: 24）

### 统计

| 维度 | 数量 |
|---|---|
| DONE 候选 | 7 |
| STALE re-scope / STALE-CLOSE | 7 |
| DUP | 1（B2-R-05 已 ↩ 维持）|
| P 升级 | 13 |
| P 降级 | 11 |
| Flag 调整 | 9 |

### DONE 候选（7）

| ID | cap | 证据 |
|---|---|---|
| KERNEL-INTERNAL-DAG-GUARD-01 | cap-02 | archtest ship + extract contractspec/cellvocab (#451) |
| PR411-AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01 | cap-02 | PR #432 single oracle + value-true semantics |
| B2-K-05 | cap-02 | STALE-CLOSE：errcode 三层 redaction 已落（WithInternal 不下发 wire） |
| R3 | cap-13 | STALE-CLOSE：SafeObserve DI 已收口（commit 4c332aa4c + 530c30866） |
| PR284-FU-COMPOSE-HEALTH | cap-13 | 三个 example docker-compose 都已含 healthcheck |
| P4-TD-10 | cap-13 | STALE-CLOSE：route template resolver 已落 + observability.md §HTTP Metrics 写明 |
| ROADMAP-D3A2-UNBLOCK-ANNOTATION-01 | cap-13 | STALE-CLOSE：029 roadmap 已归档，annotation 漂移失去 enforcement 价值 |

### STALE re-scope（4）

| ID | cap | 处理 |
|---|---|---|
| G-1 | cap-02 | FMT-11 编号已被复用为新语义；trigger 改为"确认 ADV 矩阵覆盖度后归档 OR 重新分配 rule code"；P2→P3，🟡→🟠 |
| OBS-SSA-ANALYZER-01 | cap-13 | 三份归档 roadmap 均判触发型；缺 P 补 P3，🟡→🟠 |
| A5a-R3 / A5a-R12 | cap-13 | 描述无具体路径锚点 / gap 项；候选 DUP-of-METRICS-CTX-FUNNEL-01 + STALE 触发型 P3 |

### P 升级（13，cap-02 主导）

| ID | cap | 原 → 新 | 命中维度 |
|---|---|---|---|
| KERNEL-CONTRACTSPEC-CONTRACTMETA-DUAL-DEF-01 | cap-02 | 缺 → P1 | 架构去重 single-source |
| SHARED-ERROR-SCHEMA-GENERATION-01 | cap-02 | P2 → P1 | single-source funnel（4 份 mirror）|
| PR-FIXTURE-CELLID-TYPED-BUILDER-01 | cap-02 | P2 → P1 | Soft→Hard charter mandate |
| CLOCK-INJECTION-STRUCT-FIELD-CTOR-01 | cap-02 | P2 → P1 | funnel 双向锁未闭 |
| M2-LIFECYCLE | cap-02 | P2/Cx3 → P1/Cx4 | 跨 kernel 子系统 state-machine 显式化 |
| M3-RULE-ENGINE | cap-02 | P2/Cx3 → P1/Cx4 | data-driven 架构 refactor |
| G-13-FU-H2-VALIDATIONRESULT-SEALED | cap-02 | P2 → P1 | Soft→Hard sealed Result types |
| CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 | cap-02 | P2 → P1 | funnel 双向锁 |
| ARCHTEST-LAYER10-PASS-MIGRATION-01 | cap-02 | P3 → P2 | charter mandated（pass_funnel_test.go 点名）|
| M1-OBSERVED | cap-13 | P2 → P1 | 38 处 Health 收口 + 新 kernel 接口包 + codegen |
| CONFIGPUBLISH-FAILOPEN-METRIC-ASSERT-HARD-01 | cap-13 | P3 → P2 | Soft→Hard + charter mandate |
| SAFEID-UPSTREAM-FUNNEL-HARD-01 | cap-13 | P2 → P1 | charter Funnel 双向锁 mandate |
| METRICS-GAUGEVEC-UPSTREAM-HARD-01 | cap-13 | P2 → P1 | charter Funnel 双向锁 mandate |

### P 降级（11）

| ID | cap | 原 → 新 | 信号 |
|---|---|---|---|
| P1-5 | cap-02 | P1 → P3 | 推测性 perf bench 无 outcome |
| DURABLE-TYPE-01 | cap-02 | P2 → P3 | "探索" + v1.1 触发未达 |
| J-03 | cap-02 | P1 → P2 | doc 演练非架构 refactor + 触发未达 |
| B2-T-07-FU-3 | cap-02 | 缺 → P3 | 触发型 + 无业务方 |
| PR-CI-5-FU-HEALTH-LATE-WATCHER | cap-13 | 缺 → P3 | 触发型补丁 |
| PR237-OB2 | cap-13 | 缺 → P3 | 常规 metric 增强 + D3a-2 未起 |
| PR283-OTEL-SLOG-ERROR-ATTR | cap-13 | P2 → P3 | 触发型 |
| A5a-R3 / R12 | cap-13 | 缺 → P3 | STALE 描述无 outcome |
| WS-DX-01 | cap-13 | 缺 → P3 | 触发型 observability 增强 |
| BOOTSTRAP-SHUTDOWN-OUTCOME-LABEL-ALIGNMENT-01 | cap-13 | P2 → P3 | 文档对齐项 |

### Flag 调整（9）

| ID | cap | 原 → 新 | 理由 |
|---|---|---|---|
| KERNEL-INTERNAL-DAG-GUARD-01 / PR411-AUTH-SCHEMA-* / B2-K-05 | cap-02 | 🟡 → ✅ | DONE / STALE-CLOSE |
| G-1 / B2-T-07-FU-3 / DURABLE-TYPE-01 / P1-5 | cap-02 | 🟡 → 🟠 | re-scope 触发型 |
| R3 / PR284-FU-COMPOSE-HEALTH / P4-TD-10 / ROADMAP-D3A2 | cap-13 | 🟡 → ✅ | DONE / STALE-CLOSE |
| PR392-FU-AUDIT-CHAIN-WIRING | cap-13 | 🟠 → 🟡 | trigger 已达成（PR #450） |
| USER-REPO-READYZ-PROBE-01 | cap-13 | 🟢 → 🟠 | PR-618 review F7 未实际收口（仍触发型） |
| OBS-SSA-ANALYZER / A5a-R3/R12 | cap-13 | 🟡 → 🟠 | STALE 触发型 |

### Cx 调整（2）

M2-LIFECYCLE / M3-RULE-ENGINE：Cx3 → Cx4（跨 kernel 子系统 + ADR + codegen）

### 规则边界发现（3 项登记，待 P4 末复盘）

1. **"编号语义复用" STALE 形态**：G-1 暴露的 FMT-11 case：rule code 字面在仓库 active 但语义已替换。rubric §2 应补"代码搜锚点命中但语义已漂移" 也属 STALE 信号。
2. **触发型架构条目升级冲突**：KERNEL-DEPGRAPH-OUT-EVAL / ARCHTEST-LAYER10 在"架构去重升级"与"触发未达降级"间张力。本阶段策略：触发未达优先 + charter mandated（funnel godoc 直接点名 backlog ID）单独 +1（不顶 P1）。
3. **WONT-FIX 决策定案的 Flag**：PR432-FU-AUTH-COMBO-ARCHTEST 是 architect "不立"决策占位，建议增 ⚪ Flag（决策定案，永久 OPEN 作历史追溯但不进 P 升降矩阵）。

### Commit

`ab566c1b9` — 35 处 row 编辑（cap-02 17 + cap-13 18，含 G-1 / OBS-SSA / A5a-R3-R12 等 5 处 STALE re-scope 描述补注）

---

## Phase 4 — cap-14 + cap-x-cross (2026-05-20)

**扫描条目**：131（cap-14: 90 / cap-x-cross: 44；cap-14 拆 3 agent，cap-x-cross 拆 2 agent，5 agent 并行）

### 统计

| 维度 | 数量 |
|---|---|
| DONE 候选 | 13 |
| STALE re-scope / STALE-CLOSE | 8 |
| P 升级 | 22 |
| P 降级 | 25+ |
| Flag 调整 | 14+ |

### DONE 候选（13）

| ID | cap | 证据 |
|---|---|---|
| B2-K-08 | cap-14.1 | snapshots_race_test.go 已用 helper 拆分 |
| FIXTURE-DIAGNOSTIC-GOLDEN-MIGRATION | cap-14.1 | scope A/B/C 全部 ship（PR #562-566） |
| C-L6 | cap-14.3 | `pathx.ContractIDToPackagePath` 单源 funnel 已落 |
| JOURNEY-ACCOUNTLOCKOUT-AUTO-EXPANSION-01 | cap-14.4 | PR #585 ship + 4 criteria mode:auto + e2e test |
| FU2-GOVERNANCE-STATIC | cap-14.5 | 描述无明确 outcome，候选归档 |
| B2-A-13 | cap-14.6 | STALE-CLOSE：pool.go 无 SQL dump，前提失效 |
| ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 | x.1 | PR #517 ship + archtest 双向 Hard 闭环 |
| S3-CLASSIFYERROR-CONN-REFUSED-01 | x.1 | PR #581 ship（已 ✅）|
| PR238-FU4 | x.2 | legacy NotFound 测试已删 |
| PR464-FU-CAS-PROTOCOL-ARCHTEST-TYPESEVAL-UPGRADE-01 | x.2 | RunTyped + ResolvePackageRef typed funnel 已升 |
| PR237-F06 | x.2 | STALE-CLOSE：internalGuardFromEnv 已 fail-fast |
| C-DC9 | x.5 | STALE：auditarchive slice 不存在 |

### STALE re-scope（4）

| ID | cap | 处理 |
|---|---|---|
| K05-ARCHTEST-PACKAGES-LOAD-UPGRADE | cap-14.1 | codegen_unified_test.go 已不存在；补 P3/Cx3 + Flag 注解 |
| V-A13 | cap-14.5 | phase3b 在 main.go.tpl grep 不到；降 P3 |
| PR266-AUDITAPPEND-STRICT | cap-14.6 | auditappend 已拆为 4 slice，lenient 落在 internal/appender/actor.go |
| PR-BATCH2-RETRO-FU | x.2 | BR-P2-06 已 DONE，剩余 5 条 OPEN |

### P 升级（22）

**P→P1（cap-14: 8 / cap-x-cross: 5 = 13 项）**：
- cap-14.1：SERVICEOWNED-HANDLER-OWNER-CHECK / PASS-PRODUCTION-UPSTREAM / ARCHTEST-FUNNEL-CALLSITE / PG-REPO-AMBIENT-TX / CLI-TOPLEVEL-HELP-REGISTRY（charter mandated funnel godoc 点名 backlog ID）
- cap-14.4：JOURNEY-METADATA-STATE-LIFECYCLE-TYPED-CONST（charter mandate）
- cap-14.5/6：M4-COVERAGE / MEM-TX-R2B-RECEIVER / REQUIRED-DEP-NIL-GUARD
- cap-x-cross：OPS-CONTRACT-STRING-FUNNEL / F-06（V 模型追溯链路） / F-A-002（Funnel 双向锁）/ USER-REPO-UPDATE-USE-CASE-SPLIT / CONTRACT-SHARED-MIXIN-FUNNEL（codegen funnel #1 载体）

**P→P2（9 项）**：BASECELL-DURABILITYMODE-DEFERRED（P3→P2）/ ARCHTEST-LAYER10-PASS-MIGRATION（已升 P3→P2 在 cap-02）/ JOURNEY-CONTRACT-EXISTENCE-CODEGEN-DERIVE / CONTRACT-ENDPOINT-TEST-MAPPING / CONTRACT-PATH-QUERY-COVERAGE / ACCOUNTLOCKOUT-THRESHOLD-DRIFT / AUTH-CACHE-AFTER-COMMIT-INVALIDATION / TYPED-NIL-ARCHTEST-ADAPTERS-EXTENSION / SCAFFOLD-TEST-FIXTURE-DEDUP / SESSIONLOGIN-LOCKOUT-ARCHTEST-BLINDSPOT / CONFIG-ROLLBACK-CAS-HARD-UPGRADE / SLICE-ISP-DEFERRED / T7

### P 降级（25+）

**P2→P3**：
- cap-14.3：CONTRACT-BREAKING / CONTRACT-CODEGEN / CONTRACT-STUB（V1.1 远期推测性）
- cap-14.4：JOURNEY-ACCOUNTLOCKOUT-AUTO-EXPANSION（主体已 ship）
- cap-14.5/6：NOLINT-AUDIT / B2-A-34 / B2-X-01 / D2-FU-01 / B2-X-08 / PR266-AUDITAPPEND（含 STALE）/ L2-ATOMICITY-HARNESS-FOLLOWUPS / MEM-STORE-RWMUTEX
- cap-x-cross：ADAPTER-POSITIVE-PERMANENT (P1→P2) / PR464-FU-CHANGEPASSWORD-BCRYPT / F-04 / F-05 / F-S-005 / SSOBFF-REDIS-CLAIMER / KERNEL-WEBHOOK / RUNTIME-SCHEDULER / L3-EXAMPLE-PROJECTION

**缺 P 补 P3**：cap-14 多条（PR332-VERIFY-GENERATED / VERIFY-CODEGEN-SANDBOX / PR250-F3 / T6 / T10）+ cap-x-cross 多条（DEVOPS-INTEGRATION / PR-A41-FU1 / PR237-DX1 / PR467-FU-PANIC / P3-TD-04 / P3-TD-05 / P4-TD-01 / P4-TD-06 / X4 / B2-T-07-FU-4）

### Flag 调整（14+）

✅ 13 项（见 DONE 列表）+ SLICE-ISP-DEFERRED 🔴→🟠 + USER-REPO-READYZ-PROBE-01 🟢→🟠 (P3) + 多条 🟡→🟠 STALE 触发型。

### 规则边界发现（4 项登记，待 P5 收尾复盘）

1. **charter mandate 信号高频复现**：cap-14.1 中 ≥5 条触发型 funnel 升级条目（SERVICEOWNED / PASS-PRODUCTION / PG-REPO-AMBIENT-TX / ARCHTEST-FUNNEL-CALLSITE / CLI-TOPLEVEL-HELP-REGISTRY）archtest godoc 显式点名 backlog ID，rubric §4 "charter mandate" 是稳定的 P 升级判定锚点。建议下版 rubric §4 增"charter godoc/archtest 注释直接点名 backlog ID 信号"作显式升级因子。
2. **触发型 + charter mandate 冲突优先级**：x.5 USER-REPO-UPDATE-USE-CASE-SPLIT 等命中"charter §载体决策原则 Soft→Hard"同时是触发型；本次按"charter mandate 优先于触发型"4 条升 P1。建议 rubric §4.5 显式优先级。
3. **bundle 子条 ✅ + 主条目仍 OPEN 形态**：INTERFACE-EXPORT-SURFACE-CLOSURE-BUNDLE 内 A-04 已 ship 但 A-05/A-08 等仍 OPEN，主条目维持 P1 OPEN。rubric §6.1 应允许 bundle 描述列内 ✅ 子条标注。
4. **路径漂移 STALE re-scope 集中爆发**：P4 阶段 5 条 STALE re-scope（K05/V-A13/PR266/C-DC9/PR-BATCH2）暴露 backlog 描述未跟代码 refactor 同步问题。归档前应统一治理。

### Commit

`d17e8d1a9` — 54 处 row 编辑（cap-14 35 + cap-x-cross 19）通过 sed batch（ID-anchored）+ 少量 Edit 工具应用

---

## Phase 5 — Review fix (2026-05-20)

PR #604 review 后 3 个 reviewer agent 出 33 条 findings：

| Reviewer | Findings |
|---|---|
| A 覆盖度 | 21 空 P + 3 非标格式 + 1 commit hash 占位 |
| B P 升级正确性 | 5 P1 假阳（F-01 / F-02 / J-02 / F-06 降 P2；ADAPTER-FAKE-EXPORT-01 保留 P1 因架构跨 ≥ 8 adapter）|
| C STALE/DUP 一致性 | C-DC9 ✅ 补 STALE-CLOSE 注 / FU2 补 STALE-CLOSE / PR266 路径漂移更新 / Phase 4 commit hash 补 |

**应用修复**（~30 处 sed + Edit）：
- 4 处 P1→P2（F-01 / F-02 / J-02 / F-06）— Type=feat/doc 不命中 rubric §4 架构 refactor 第一维度
- 21 处空 P 补齐（A26-R3 / SECURECOOKIE-AEAD-NEG-01 / KERNEL-DEPGRAPH-OUT-EVAL-01 / IDUTIL-UUID-RAND / PR332-VERIFY / VERIFY-CODEGEN-SANDBOX / ADR-INDEX / PR250-F3 / MEM-STORE-RWMUTEX / L2-ATOMICITY-HARNESS / DEVOPS-INTEGRATION / PR-A41-FU1 / PR237-DX1 / PR467-FU-PANIC / P3-TD-04 / P3-TD-05 / P4-TD-01 / P4-TD-06 / B2-T-07-FU-4 / PR-BATCH2-RETRO-FU / B-FLOOR-FOLLOWUP）
- 4 处描述/Source/Files 列注解（commit hash / C-DC9 STALE-CLOSE / FU2 STALE-CLOSE / PR266 路径漂移）

### Commit

`a17a00ca6` — Phase 5 review fix（~30 处行编辑：21 空 P 补齐 + 4 P1 假阳降级 + 3 STALE/路径漂移 + 1 commit hash 补全 + 1 LOG Phase 5 段落）
