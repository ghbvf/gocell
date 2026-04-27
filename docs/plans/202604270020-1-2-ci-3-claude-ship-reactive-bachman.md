# L4 计划：反复模式根治（CI 模式 + 业务消化合并 + Claude 分层规则 + v1 schema ADR）

## Context

2026-04-26 分层 6 角色全仓审查（`bak/20260426-layered-six-role-review/`）暴露 **31 条 P1**，跨 7 层。逐条归因后发现 **8 个反复模式**：fail-open 默认值（8 处）/ 双源漂移（7 处）/ 失败语义不一致（4 处）/ 测试假绿（4 处）/ 错误传播不完整（3 处）/ 边界泄漏（2 处）/ 声明 ≠ 实现（3 处）/ 调度可观测弱（1 处）。

PR-CFG-G1 review 的 8 条 FU 是同型现象的**缩小版预览**——任何 ≥10 文件大 PR 都会暴露 5-10 条同型问题。本次 31 条是 6 个月累积态。

**根因**（系统性）：
1. 缺 fail-closed 治理规则；缺 SoT 约束；失败语义无 ADR；测试基础设施宽容
2. 大 PR review 信号迟到，单 PR review 把模式问题打 OUT_OF_SCOPE → 跨 PR 累积
3. v1 schema strict（`additionalProperties:false`，PR-CFG-E #278 落地）与 `.claude/rules/gocell/api-versioning.md` "v1 只增不删" 硬冲突——需 ADR 决策

**目标**：
1. **CI 模式治理（治本）**：8 条 archtest 静态规则消除 ~14 条 P1 未来复发（58% 反复模式自动拦截）
2. **业务消化合并**：每个 archtest 规则 + 同 PR 内消化该规则触发的全仓业务代码 fail，**CI 始终绿**（用户硬约束）
3. **Claude 分层规则升级**：参考 `docs/new-setting/` 渐进式披露方案，建立分层骨架 + 把反复模式守护写入对应层 CLAUDE.md
4. **v1 schema 演进 ADR**：方向 A vs B 一并决策

**对标**：
- PR-A49 #290 `tools/archtest/topic_const_resolver.go` `packages.Load + go/types TypesInfo` 范式（全部新规则复用）
- `docs/new-setting/` 渐进式披露目录（已设计未应用）
- Stripe / GitHub REST v1 演进策略；OpenAPI 3.x `additionalProperties` unset 推荐

---

## 用户硬约束

1. **每个 CI 模式 1 个 PR**：archtest 规则 + 同 PR 内消化全仓业务 fail 一次性全量修完，**CI 始终绿**（不要"先合规则让 CI 红，再批量修"）
2. **不向后兼容**：每条规则一次性全量处理；不留 waiver / opt-in / `--lenient` flag / `//nolint:archtest-XXX` 注释
3. **claude 规则分层**：参考 `docs/new-setting/` 风格；本 plan 只**先写大概骨架** + 反复模式占位条款，详细内容后续 plan
4. **v1 schema ADR 一起决策**：不再排到 backlog，纳入本 plan 决策

## 当前完成状态（2026-04-27）

**基准**：最近合并 PR 为 #302（`b5131358`，2026-04-27 18:22 CST），`origin/develop` 已到该提交；本地 `develop` 尚落后 1 个提交，未在本次文档更新中 rebase。

| 阶段 | 状态 | PR / 依据 | 备注 |
|---|---|---|---|
| 阶段 0 Baseline 同步 | ✅ 已完成 | #291 / #292 已合入，后续 Phase 1 PR 均基于更新后的 `develop` 合并 | 当前本地工作区有未提交改动，未执行 pull/rebase |
| 阶段 1 CI 模式治理 | ✅ 已完成（8/8） | #293 / #294 / #295 / #296 / #297 / #298 / #300 / #302 | #301 为 review hardening 补充闭环，不单独占 Phase 1 名额 |
| 阶段 2 Claude 分层规则骨架 | ⏳ 未开始 | 未找到 `PR-CLAUDE-LAYERED` 合并 PR；`origin/develop` 上 6 个层级 `CLAUDE.md` 仍缺失 | 下一步 |
| 阶段 3 v1 Schema 演进 ADR | ⏳ 未开始 | 未找到 `PR-V1-EVOLVE-ADR` 合并 PR；`docs/architecture/202604260000-v1-schema-evolution.md` 仍缺失 | 阶段 1 后执行 |
| 阶段 4 6 角色 retrospective | ⏳ 未开始 | 依赖阶段 2 + 阶段 3 完成 | 最终验收 |

---

## 范围与依赖

### 范围内
- 8 条 archtest 治理规则（每条 1 PR + 业务消化）
- Claude 分层规则骨架（kernel/runtime/adapters/cells/pkg/contracts 各一 CLAUDE.md）
- v1 schema 演进 ADR（PR-V1-EVOLVE-ADR）
- review skill / fix skill / memory 升级
- 6 角色 retrospective

### 范围外（保留 backlog1.md §2.4-2.7 既有节奏）
- PR-LIFECYCLE-ROBUSTNESS / PR-TEST-DEPTH / PR-API-CONSISTENCY / PR-CLI-CONSISTENCY — 属于"不能 CI 化的 26%" 中的源码改造，按 backlog1.md 排期
- backlog1.md §3 既有 PR 扩充（PR-A53/A41/PR252-F2）— 按既有 plan 节奏

---

## 实施切分（4 阶段）

### 阶段 0 — Baseline 同步（10 min，✅ 已完成）

```bash
git fetch origin && git pull --rebase   # 整合 G1 #292 / G2 #291 合并状态
git log --oneline -5
go build ./... && go test ./... -count=1
go run ./cmd/gocell validate --strict   # 当前基线 0 violations
```

确认 baseline；G1/G2 合并状态影响后续 PR rebase 但不影响规则定义。

---

### 阶段 1 — CI 模式治理（**8 个 PR，每个 1 模式 + 业务修复**，~62h，✅ 已完成）

**抽象**：每个 PR = 1 archtest 规则 + 该规则触发的全仓业务代码 fail 一次性全量消化。**CI 始终绿**（开 PR 前先在 worktree 跑通 archtest 0 violations）。

**纵切原则**：每条规则的 fail 全部纳入同一 PR，不出现"先修 cells 留 runtime follow-up"半截状态。

**PR 顺序**（安全优先 → 漂移 → 一致性 → 健壮性 → 边界 → 验收）：

| PR | 状态 | 规则 + 落点 | 业务消化范围 | 工时 |
|---|---|---|---|---:|
| **PR-MODE-1** SEC-FAIL-CLOSED | ✅ #297 已合并 | 新 `tools/archtest/security_defaults_test.go`（4 sub-test）| 修 `cmd/corebundle/{controlplane,bundle}.go` 控制面 addr-driven；`runtime/bootstrap/listener.go` internal/health 强制 auth chain；`runtime/http/health/health.go` verbose token fail-closed；`adapters/{redis,vault,s3}` Config endpoint 强制 TLS；`adapters/websocket/handler.go` Origins required；`examples/*/main.go` 同步修 demo | 12h |
| **PR-MODE-2** META-SUBSCRIPTION-DRIFT | ✅ #294 已合并 | `kernel/governance/rules_advisory.go` 扩 ADV-06 | 修 `cells/auditcore/slices/auditappend/slice.yaml` contractUsages 同步实际订阅；扫全仓其他 cells 的同型漂移并修 | 5h |
| **PR-MODE-3** META-QUERYPARAM-DRIFT | ✅ #300 已合并 | 新 `tools/archtest/queryparam_drift_test.go` | 修 `cells/auditcore/slices/auditquery` contract.yaml + handler 双向同步；`cells/accesscore/slices/identitymanage/handler.go` user patch 改 strict DTO；扫全仓同型漂移 | 8h |
| **PR-MODE-4** CONTRACT-CLIENTS-AUDIENCE | ✅ #293 已合并 | `kernel/governance/rules_ref.go` 新 REF-17 | 修 `contracts/http/auth/role/{assign,revoke,list}/v1/contract.yaml::endpoints.clients`（含 backlog G2-FU1）；扫全仓 internal path 但 clients 含 external actor 的所有点 | 4h |
| **PR-MODE-5** CONTRACT-INPUT-CONSTRAINT | ✅ #298 已合并 | `kernel/governance/rules_strict.go` 扩 FMT-INPUT-CONSTRAINT-01 | 全仓 HTTP request schema 字符串字段加 `minLength`/`maxLength`；list endpoint `limit` 加 `maximum: 500` | 5h |
| **PR-MODE-6** ERROR-FIRST-API | ✅ #296 已合并 | 新 `tools/archtest/error_first_test.go` | 修 `kernel/wrapper/{handler,spec,auth_plan}.go` 改 error-first + 提供 `Must*` 包装；`adapters/postgres/refresh_store.go`；`runtime/eventrouter/router.go` RegisterSubscriptions panic→error；`runtime/worker/worker.go` nil 退出建模为 `ErrWorkerExitedEarly`；architectural panic 白名单 ≤ 5 点 ADR 标注 | 14h |
| **PR-MODE-7** CELL-PORTS-PURE | ✅ #302 已合并 | `tools/archtest/layer_test.go` 扩 LAYER-10 | 修 `cells/configcore/{cell,cell_init}.go` Cell 公共 API 不再暴露 `*adapterpg.Pool`；`cmd/corebundle/{configcore,access,audit}_module.go` composition root 装配责任回收；扫全仓同型边界泄漏 | 10h |
| **PR-MODE-8** JOURNEY-AUTO-CHECK | ✅ #295 已合并 | `kernel/governance/rules_verify.go` 扩 VERIFY-06 | strict 模式要求 `lifecycle: active` journey ≥1 auto check；修 `journeys/J-*.yaml` 全部加 auto check 或标 experimental（结合 backlog `PR220-3`）| 4h |
| **合计** | ✅ 8/8 已合并 | — | — | **62h** |

**关键复用**：
- `tools/archtest/topic_const_resolver.go::ResolveString` — 所有 archtest 复用
- `kernel/governance/rules_advisory.go::ADV-05` event consumer 框架 — PR-MODE-2 直接扩
- `kernel/governance/rules_strict.go::ValidateStrict(bool)` — PR-MODE-5/8 复用
- `actors.yaml::type: external` — PR-MODE-4 直接读取（4 个 external actor 现成）

**单 PR 内闭环验证（CI 始终绿前提）**：
```bash
# worktree 中开 PR 前必跑
go build ./... && go test ./tools/archtest/... -race -count=1
go run ./cmd/gocell validate --strict   # 期望 0 errors
golangci-lint run ./...                 # 0 issues
go test -tags=integration ./adapters/... ./cmd/corebundle/... -timeout 15m
```

**PR 间并行性**：
- PR-MODE-1（cmd/runtime/adapters）vs PR-MODE-2（cells auditappend slice.yaml）vs PR-MODE-4（contracts/auth/role） — 文件域 ∅，**3 路并行**
- PR-MODE-3（cells handler + contract）vs PR-MODE-5（contracts schema）vs PR-MODE-7（cells/configcore + cmd/corebundle module） — 部分交集，**串行**
- PR-MODE-6（kernel/wrapper + runtime/eventrouter + runtime/worker + adapters/postgres）— 独立 worktree，可与其他并行
- PR-MODE-8（journeys/）— 独立，并行任意

**反方观点（已驳回）**：
- "为什么不一次合 8 条让 CI 红？" → 用户硬约束 CI 始终绿；按模式 1 PR 也避免单 PR diff 过大失焦
- "PR-MODE-6 14h 是否拆？" → 同模式不拆，避免 panic→error 跨 PR 半截

---

### 阶段 2 — Claude 分层规则骨架（**单 PR `PR-CLAUDE-LAYERED`**，~4h，⏳ 未开始）

**抽象**：参考 `docs/new-setting/` 已设计未应用的分层方案，**先写大概骨架** + 把反复模式守护作为占位条款写入对应层 CLAUDE.md，**详细内容后续 plan 再展开**。

**目标**：
- 建立分层目录结构（kernel/CLAUDE.md 等 6 个层 CLAUDE.md）
- 反复模式守护占位（每条 1-3 行 + TODO 标注）
- review/fix skill 维度补全
- memory 强约束 4 个反复模式 IN_SCOPE

**改动清单**（基于 Phase 1 探索 + `docs/new-setting/` 模式）：

| 文件 | 内容 | 状态 |
|---|---|---|
| `kernel/CLAUDE.md`（新） | 大概骨架 + **Panic 禁止列表**（占位 1-2 段，TODO 后续详化） | 复制 `docs/new-setting/kernel/CLAUDE.md` 现有骨架 + 加 Panic 段 |
| `runtime/CLAUDE.md`（新） | 大概骨架 + **Fail-Closed 默认值原则**（占位）+ Composition Root 模式 | 复制 + 加段 |
| `adapters/CLAUDE.md`（新） | 大概骨架 + **构造器 error-first** + **TLS 强制（real mode）** + **连接预算** 占位 | 复制 + 加段 |
| `cells/CLAUDE.md`（新） | 大概骨架 + **Metadata 声明同步** + **边界纯化（不暴露 adapter 类型）** 占位 | 复制 + 加段 |
| `pkg/CLAUDE.md`（新） | 大概骨架 | 复制 |
| `contracts/CLAUDE.md`（新） | 大概骨架 + **v1 演进策略（指向 ADR）** 占位 | 复制 + 加段 |
| 根 `CLAUDE.md` | 精简 + 加"渐进式披露：进入子目录加载对应 CLAUDE.md" 说明；保留跨层规则 | 改 |
| `.claude/rules/gocell/eventbus.md` | L51 后补 **Fail-Closed 原则**（consumer init / unmarshal / 幂等三段，占位 1-2 行 + TODO）| 扩 |
| `.claude/rules/gocell/go-standards.md` | L101 后新 **Panic 禁止列表**（kernel/cells/runtime/adapters 禁 panic 占位）| 扩 |
| `.claude/agents/reviewer.md` | "运维/部署" 维度后加 3 项：Consumer 生命周期 / Zero-value 安全 / Panic 扫描 | 扩 |
| `.claude/skills/fix/SKILL.md` | L52 批量模式 Triage 步骤 4 后加 **反复模式标签**（[FAIL-OPEN] / [PANIC-RISK] / [METADATA-DRIFT] / [TEST-QUALITY] / [BOUNDARY-LEAK] 5 标签）| 扩 |
| `~/.claude/projects/-Users-shengming-Documents-code-gocell/memory/feedback_recurring_patterns_strict_inscope.md`（新）| 升级既有 `feedback_pr_findings_default_inscope.md`：8 个反复模式之一被命中时**禁止 OUT_OF_SCOPE / P2 / follow-up**；列 8 模式清单 | memory 新建 |
| `~/.claude/projects/-Users-shengming-Documents-code-gocell/memory/feedback_metadata_declaration_integrity.md`（新）| Metadata 声明 ↔ 实现同步约束（slice.yaml allowedFiles ↔ find；contractUsages ↔ AddHandler）| memory 新建 |

**"先写大概，后续详细"原则**：
- 各层 CLAUDE.md 仅写骨架 + 反复模式守护**占位条款 1-2 行 + TODO 注释**
- 不在本 PR 完整迁移 `.claude/rules/gocell/*.md` 内容到层 CLAUDE.md（那是后续独立 plan 工作）
- review/fix skill 升级用最小增量（5-10 行新增），不重构整个 SKILL.md

**验收**：
- 6 个层 CLAUDE.md 文件存在且含反复模式占位段
- `golangci-lint run ./...` 0 issues（不影响代码）
- 新 memory 文件存在；`/clear` 后召回测试

**反方观点（已采纳）**：
- "为什么不一次完整迁移 docs/new-setting/ 内容？" → 用户明确"先写大概后续详细"；本 PR 只建结构，避免大动 .claude/rules/* 引发多场景回归
- TODO 标注便于下一轮 plan 展开

---

### 阶段 3 — v1 Schema 演进 ADR（**单 PR `PR-V1-EVOLVE-ADR`**，~5h，⏳ 未开始）

**抽象**：v1 响应 schema 普遍 `additionalProperties: false`（PR-CFG-E #278 加，对应 contracts 报告 P1.2）与 `.claude/rules/gocell/api-versioning.md:12` "v1 只增不删 / 新增可选字段不破坏 v1" 硬冲突。决策方向 + 落地。

**两个决策方向对比**：

| 维度 | 方向 A（响应放宽） | 方向 B（保持 strict） |
|---|---|---|
| 实施 | response schema `additionalProperties: true / unset`，request schema 保持 false；治理规则 `FMT-RESPONSE-STRICT-01` 改分输入/输出 | 保持响应 strict；改 `api-versioning.md` "v1 只增不删" → "v1 只能加新端点不能加新字段（加字段必须 v2）" |
| 演进自由 | ✅ 可加可选字段，不破 v1 客户端 | ❌ 任何新字段都触发 v2 迁移成本 |
| 兼容性测试 | ⚠️ 测试覆盖度变弱（contract test 不强制字段全集） | ✅ 强 schema 锁定 |
| 业界对标 | Stripe API / GitHub REST v3 / OpenAPI 3.x 推荐 unset | 仅 strict gRPC schema 模式（不适合 HTTP） |
| 与 PR-CFG-E #278 关系 | **反向**（需治理规则部分回退）| ✅ 顺向延续 |
| 工时 | ~5h（ADR + 治理规则改 + 测试改）| ~3h（ADR + api-versioning.md 改 + 现状不动） |

**推荐方向 A**（业界一致 + v1 自由演进）。

**实施改动**（方向 A 落地）：
- 新建 `docs/architecture/202604260000-v1-schema-evolution.md`（ADR）
- 改 `kernel/governance/rules_strict.go::FMT-RESPONSE-STRICT-01` → 分裂为：
  - `FMT-REQUEST-STRICT-01`：HTTP request schema `additionalProperties: false`（保持）
  - 删 response 维度的 strict 强制
- 全仓 `contracts/http/*/v1/response.schema.json` 移除顶层 `additionalProperties: false`（保留 items 内严格）
- `pkg/contracttest/` validate 逻辑同步：response 不强制未知字段拒绝
- 更新 `.claude/rules/gocell/api-versioning.md` 描述明确"响应可加新字段，请求保持严格"

**反方观点**：方向 A 与 PR-CFG-E #278 落地方向反 → 不算回退，是策略修正（review 当时已识别 trade-off 但选了 strict，本次决策更新）。

**验收**：
- ADR 文件存在
- `gocell validate --strict` 通过新规则
- 现有 contract test 全绿
- 加 1 条新 contract test：response 多 1 个字段不应触发 reject

---

### 阶段 4 — 6 角色 retrospective（**L4 强制，~3h，⏳ 未开始**）

**抽象**：阶段 1+2+3 累计 diff 整体走 6 角色并行 review，确认无新反复模式漏网。

**6 角色矩阵**：架构 / 安全 / 测试 / 运维 / 可维护性 / 产品

**输出**：`docs/reviews/202604XXXX-recurring-patterns-retrospective.md`，包含：
- 8 条规则在生产代码上的真实拦截统计（修了多少处 fail）
- review skill 升级后下一个 PR 的实测效果（如时间允许，跑一个真 PR review 验证维度生效）
- 剩余技术债与下一轮治理建议

---

## 关键文件路径速查

### 阶段 1（8 PR，每 PR 一组）

**PR-MODE-1**：
- 新：`tools/archtest/security_defaults_test.go`
- 改：`cmd/corebundle/{controlplane,bundle}.go`、`runtime/bootstrap/listener.go`、`runtime/http/health/health.go`、`adapters/{redis,vault,s3}/`、`adapters/websocket/handler.go`、`examples/*/main.go`

**PR-MODE-2**：扩 `kernel/governance/rules_advisory.go` ADV-06；改 `cells/auditcore/slices/auditappend/slice.yaml` + 全仓订阅声明同步

**PR-MODE-3**：新 `tools/archtest/queryparam_drift_test.go`；改 `cells/auditcore/slices/auditquery/{handler.go,contract.yaml}` + `cells/accesscore/slices/identitymanage/handler.go`

**PR-MODE-4**：扩 `kernel/governance/rules_ref.go` REF-17；改 `contracts/http/auth/role/*/v1/contract.yaml`、`actors.yaml`

**PR-MODE-5**：扩 `kernel/governance/rules_strict.go`；改全仓 `contracts/http/*/v1/request.schema.json`

**PR-MODE-6**：新 `tools/archtest/error_first_test.go`；改 `kernel/wrapper/`、`adapters/postgres/refresh_store.go`、`runtime/eventrouter/router.go`、`runtime/worker/worker.go`

**PR-MODE-7**：扩 `tools/archtest/layer_test.go` LAYER-10；改 `cells/configcore/{cell,cell_init}.go`、`cmd/corebundle/{configcore,access,audit}_module.go`

**PR-MODE-8**：扩 `kernel/governance/rules_verify.go` VERIFY-06；改 `journeys/J-*.yaml`

### 阶段 2（PR-CLAUDE-LAYERED）
- 新：`{kernel,runtime,adapters,cells,pkg,contracts}/CLAUDE.md`（6 个，从 `docs/new-setting/` 复制 + 反复模式占位段）
- 改：根 `CLAUDE.md` + `.claude/rules/gocell/{eventbus,go-standards}.md` + `.claude/agents/reviewer.md` + `.claude/skills/fix/SKILL.md`
- 新 memory：`feedback_recurring_patterns_strict_inscope.md` + `feedback_metadata_declaration_integrity.md`

### 阶段 3（PR-V1-EVOLVE-ADR）
- 新：`docs/architecture/202604260000-v1-schema-evolution.md`
- 改：`kernel/governance/rules_strict.go`、全仓 `contracts/http/*/v1/response.schema.json`、`pkg/contracttest/`、`.claude/rules/gocell/api-versioning.md`

---

## 复用 Pattern 速查

```go
// tools/archtest/topic_const_resolver.go::ResolveString — 跨包常量评估范式
func (r *topicConstResolver) ResolveString(pkg *packages.Package, expr ast.Expr) (string, bool) {
    tv, ok := pkg.TypesInfo.Types[expr]
    if tv.Value == nil || tv.Value.Kind() != constant.String {
        return "", false
    }
    return constant.StringVal(tv.Value), true
}

// packages.Load 配置
cfg := &packages.Config{Mode: packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax}
pkgs, err := packages.Load(cfg, "./cells/...")
```

PR-MODE-1（adapter Config endpoint scheme）/ PR-MODE-2（topic 常量传播）/ PR-MODE-3（queryParam 字符串提取）/ PR-MODE-7（cell API 类型签名）— **全部复用此范式**。

---

## 工时与排期

| 阶段 | PR 数 | 工时 | 节奏 | 累计 |
|---|---:|---:|---|---:|
| 0 | ✅ — | 0.2h | 已完成 | 0.2h |
| 1 | ✅ 8（CI 模式 + 业务消化）| 62h | 已完成（#293-#298 / #300 / #302） | 62h |
| 2 | ⏳ 1（PR-CLAUDE-LAYERED 骨架）| 4h | 未开始 | 66h |
| 3 | ⏳ 1（PR-V1-EVOLVE-ADR）| 5h | 阶段 1 后，未开始 | 71h |
| 4 | ⏳ 0（retrospective） | 3h | 等阶段 2/3 完成后串行 | **74h** |

**双人并行**：~5 工作日；单人全工时 ~74h

---

## 决策点（已采用方案）

| 决策 | 已选方案 | 理由 |
|---|---|---|
| 一次合 8 archtest vs 1 模式 1 PR | **1 模式 1 PR + 同 PR 业务消化** | 用户硬约束（CI 始终绿）|
| Claude 规则细化 vs 骨架 | **先骨架 + 占位条款 + TODO** | 用户硬约束（先大概后详细）|
| v1 schema 方向 A vs B | **方向 A（响应放宽）** | 业界一致 + v1 自由演进；推荐用户拍板 |
| Strict ON 默认 vs opt-in | **Strict ON 默认无 flag** | 用户硬约束（不向后兼容）|
| Architectural panic 白名单 | **≤ 5 点 ADR 标注** | ERROR-FIRST-API-01 现实需要，但严格控制 |
| PR 间是否并行 | **3 路并行**（文件域 ∅ 的 PR）| 节省 ~2 工作日 |

---

## 验证矩阵

```bash
# 阶段 0
git fetch origin && git pull --rebase
go build ./... && go test ./... -count=1

# 每个阶段 1 PR 开 PR 前（必须 CI 绿）
go test ./tools/archtest/... -race -count=1
go run ./cmd/gocell validate --strict   # 0 errors / 0 warnings
golangci-lint run ./...                 # 0 issues
go test -tags=integration ./adapters/... ./tests/integration/... ./cmd/corebundle/... -timeout 15m
go test -tags=e2e ./tests/e2e/...

# 阶段 2 后
ls {kernel,runtime,adapters,cells,pkg,contracts}/CLAUDE.md   # 6 个文件存在
grep -l "反复模式\|Fail-Closed\|Panic 禁止" {kernel,runtime,adapters,cells,pkg,contracts}/CLAUDE.md
ls ~/.claude/projects/-Users-shengming-Documents-code-gocell/memory/feedback_recurring_patterns_strict_inscope.md

# 阶段 3 后
ls docs/architecture/202604260000-v1-schema-evolution.md
go test ./pkg/contracttest/... -count=1   # response 多字段不 reject 用例 PASS

# 阶段 4 retrospective
ls docs/reviews/202604*-recurring-patterns-retrospective.md
```

---

## Out of Scope

- 详细迁移 `docs/new-setting/` 到真实 `kernel/CLAUDE.md` 等（本 plan 只建骨架）— 留下一轮 plan
- backlog1.md §2.4-2.7 的 PR-LIFECYCLE-ROBUSTNESS / PR-TEST-DEPTH / PR-API-CONSISTENCY / PR-CLI-CONSISTENCY — 既有节奏排期
- backlog1.md §3 既有 PR 扩充（PR-A53/A41/PR252-F2）
- 触发条件项 Wave 9/10
- **PR-MODE-2 范围澄清**：「代码 ↔ slice.yaml 漂移」（即 `RegisterSubscriptions`/`AddHandler` 实际订阅与 `slice.yaml::contractUsages` 不一致）**留作未来另立 archtest**，不在 PR-MODE-2 内。理由：PR-MODE-2 走 `kernel/governance` ADV-06 纯 YAML 双源（`contract.yaml::endpoints.subscribers` ↔ `slice.yaml::contractUsages[role=subscribe]`），当前 contract.yaml 是 ground truth，三方对齐由 ADV-06 双向 + 既有 ADV-05 共同保证；引入 archtest 跨层（依赖 `golang.org/x/tools/go/packages`）会扩大 PR 范围且违反「1 模式 1 PR」原则。
