## GoCell Backlog Capability Framework v1（提案）

| 项 | 值 |
|---|---|
| 日期 | 2026-05-07 |
| 状态 | **提案 — 等待用户确认；满意后再迁移内容** |
| 主轴来源 | `docs/reviews/capabilities/20260504-engineering-capability-domain-map.md` §1（14 capability units）|
| 替代目标 | `docs/backlog.md` + `docs/backlog1.md` + `docs/backlog2.md` + `docs/backlog_later_detail.md` + `docs/tech-debt-registry.md`（合计 1282 行 / 4 套分类轴） |
| 不替代 | `docs/plans/*` 路线图、`docs/reviews/*` 审查报告 — 这两类是上游来源，不是 backlog |

---

## 1. 设计目标

1. **单源** — 一份权威 backlog；草案/分层/域别清单全部并入或归档。
2. **稳定主轴** — 用 capability 而非优先级作分类主轴；优先级会变，能力划分不变。
3. **机器可读** — 每条 item 含结构化 metadata，未来能 grep 出"P1 + Authn 域 + Cx2"。
4. **自然归档** — DONE 自动滚出主文件，避免 backlog 越堆越长（当前 backlog.md 60% 是历史）。
5. **零重复** — 一条 item 只在一处出现，跨能力归到主能力 + 副 capability tag。

非目标：
- 不替代 plan / roadmap（plan 描述"怎么打包成 PR"，backlog 描述"该做什么"）
- 不替代 ADR（ADR 记录决策，backlog 记录待办）

---

## 2. 物理结构

**推荐：单文件 + 归档目录**

```
docs/backlog.md                   ← 主入口，按 14 能力分章节（保留路径，工具/链接不需改）
docs/backlog/
  README.md                       ← 框架说明 + 索引导航（新建）
  archive/
    2026-q2-completed.md          ← DONE 归档（按季度）
    legacy-backlog1-20260426.md   ← 老 backlog1 整体压扁存档
    legacy-backlog2-20260429.md   ← 老 backlog2 整体压扁存档
    legacy-backlog-later.md       ← 老 backlog_later_detail
    legacy-tech-debt-registry.md  ← 老 tech-debt-registry
```

**否决方案**：
- ❌ `docs/backlog/capability-NN-*.md` 14 个单独文件 — 每文件平均 ~10KB 太碎，跨能力查找成本高
- ❌ 完全不动现有路径只加交叉索引 — 不解决草案不回灌的根本问题

---

## 3. 主轴：14 capability 章节（强制）

每条 OPEN backlog item 必须挂在恰好一个 primary capability 之下。涉及多能力时挑"主拥有者"（owner cell / 主修改包归属）。次要能力作 tag 引用，不重复列。

| # | Capability | 章节 ID | 主要包归属 |
|---|---|---|---|
| 1 | Cell 声明与生命周期 | `cap-01-cell-lifecycle` | `kernel/cell` + `assembly` + `lifecycle` |
| 2 | 元数据解析与治理 | `cap-02-metadata-governance` | `kernel/metadata` + `governance` + `verify` + `tools/archtest` |
| 3 | Contract 注册与发现 | `cap-03-contract-registry` | `kernel/wrapper` + `kernel/registry` + `pkg/contracts` |
| 4 | HTTP 入站处理 | `cap-04-http-inbound` | `runtime/http/{router,middleware,health,devtools}` |
| 5 | 身份认证 (Authn) | `cap-05-authn` | `runtime/auth` + `auth/refresh` + `auth/config` |
| 6 | 授权决策 (Authz) | `cap-06-authz` | `runtime/auth` (authz/policy) |
| 7 | 事务性事件发布 (Outbox Producer) | `cap-07-outbox-producer` | `kernel/outbox` + `runtime/outbox` + `adapters/postgres` |
| 8 | 异步事件消费 (Subscriber+Claimer) | `cap-08-subscriber-claimer` | `kernel/{outbox,idempotency}` + `runtime/eventrouter` + `adapters/{redis,rabbitmq}` |
| 9 | 配置加载与热更新 | `cap-09-config-watcher` | `runtime/config` + watcher |
| 10 | 持久化与加密 | `cap-10-persistence-crypto` | `kernel/persistence` + `kernel/crypto` + `adapters/{postgres,vault}` |
| 11 | 分布式锁 | `cap-11-distlock` | `runtime/distlock` + `adapters/redis` |
| 12 | 启停编排 (Bootstrap) | `cap-12-bootstrap` | `runtime/bootstrap` + `runtime/shutdown` |
| 13 | 可观测性 | `cap-13-observability` | `runtime/observability/*` + `pkg/logutil` + `adapters/{prometheus,otel}` |
| 14 | 代码生成与治理工具链 | `cap-14-codegen-tooling` | `tools/{archtest,codegen,depgraph,e2egate,...}` + `cmd/gocell` |
| X | 横切项 | `cap-x-cross` | 工程基线、CI/lint、文档、跨能力重构 |

---

## 4. Item schema

每条 item 必须用如下 markdown 结构：

````markdown
### [STATE] ID — 标题一句话

| Field | Value |
|---|---|
| Capability | `cap-NN-xxx` (primary) + `[cap-MM, cap-PP]` (secondary) |
| Priority   | P0 / P1 / P2 / P3 |
| Complexity | Cx1 / Cx2 / Cx3 |
| Flag       | 🔴 硬约束 / 🟠 条件延后 / 🟡 可延后 / 🟢 已纳入 plan |
| Trigger    | （仅 🟠 必填）触发条件 |
| EstHours   | 数字 / 区间 / "—" |
| Files      | 主要涉及文件路径（≤ 3 个最关键） |
| Source     | PR# / review 报告路径 |

**现状**: ...
**修复方向**: ...
````

字段约束：
- `STATE` ∈ {OPEN, IN_PROGRESS, DONE, WONTFIX}（章节内按 STATE 二级分组，不写在表内重复）
- `ID` 沿用现有命名（不强制改）；新增 item 用 `<CAP_NUM>-<DOMAIN>-<NNN>` 例如 `05-AUTHN-001`
- `Priority` 删除则视为 P3
- `Flag` 删除则视为无标记（normal）

---

## 5. State machine

```
OPEN ──→ IN_PROGRESS ──→ DONE
  │                       │
  │                       ↓
  └─→ WONTFIX        ARCHIVED（30 天后归档）
```

- **OPEN** — 未开始
- **IN_PROGRESS** — 关联 PR 已开（PR# 必填到 Source）
- **DONE** — PR 已合并；保留在主文件 30 天供 review 上下文，之后转 archive
- **WONTFIX** — 立即归档，理由必填

---

## 6. 章节模板（每个 capability 章节）

````markdown
## cap-NN: <能力名>

> 主要包: ...
> domain-map ref: §1.A #N / §1.B #N
> Open: X | In-progress: Y | Done<30d: Z | Wontfix: W

### OPEN

[items, 按 Priority 升序]

### IN_PROGRESS

[items, 按 PR# 倒序]

### TRIGGER-CONDITIONAL

[Flag=🟠 的 item 单列；触发未到时不与 OPEN 混排]

### DONE (近 30 天)

[items, 按合并日期倒序]
````

---

## 7. 顶部索引（自动派生 / 手维护）

```markdown
| # | Capability | OPEN | IN_PROGRESS | TRIGGER | DONE<30d | 跳转 |
|---|---|---|---|---|---|---|
| 1 | Cell 生命周期 | 3 | 0 | 1 | 2 | [↓](#cap-01-cell-lifecycle) |
| 2 | 元数据治理 | 5 | 1 | 0 | 4 | [↓](#cap-02-metadata-governance) |
| ... | | | | | | |
```

**派生方式**（决策点 D-1）：
- 选项 A 手动维护（简单，可能漂移）
- 选项 B 写一个 `tools/backlog/index.go` 扫所有章节 + ` ### [STATE] ` 前缀计数，由 `make backlog-index` 触发
- 推荐 A 起步，漂移成痛点再升 B

---

## 8. 横切项 cap-x-cross

不属于单一 capability 的项：
- 工程基线（CI/lint/golangci.yml/depguard）
- 跨能力大重构（K#01 typed depgraph、K#03 vocabulary collapse 这类）
- 文档/工时汇总/release readiness checklist

cap-x-cross 章节内部按主题二级分组（CI / 重构 / 文档 / 发布 / 工时）。

---

## 9. Archive policy

| 项 | 触发条件 | 操作 |
|---|---|---|
| DONE item | merge 后 ≥ 30 天 | 移到 `docs/backlog/archive/<year>-q<N>-completed.md` |
| WONTFIX item | 状态切换即时 | 移到 archive，理由保留 |
| 章节累积 ≥ 50 条 OPEN | 不自动 split | 重新审视 capability 是否拆得不够细，**人工**决定 |
| 老 backlog/backlog1/backlog2/etc | 迁移完成时一次性 | 整体压扁存到 `docs/backlog/archive/legacy-*` 备查 |

DONE 不立即归档的理由：30 天窗口内还在做 PR follow-up review，需要上下文留在主文件。

---

## 10. 迁移阶段（实施时）

| Phase | 操作 | 风险 | 工时 |
|---|---|---|---|
| **P1 骨架** | 建空的 14 章节 + cap-x-cross + 顶部索引 + `docs/backlog/README.md` + `archive/` 空目录 | 低 | 1h |
| **P2 backlog1 迁移** | backlog1.md OPEN 项归类到能力章节；DONE 项进 legacy archive | 低（草案，无外部依赖）| 2h |
| **P3 backlog2 迁移** | 同上，backlog2.md 处理；§10 索引段直接弃 | 低 | 3h |
| **P4 backlog.md 拆分** | OPEN 项归类、DONE 项进 archive、原文件就地重写为新结构 | 中（要小心 ID 不丢）| 4-6h |
| **P5 tech-debt-registry 并入** | TECH/PRODUCT 标签映射到 capability + 一行 OPEN 进章节 | 低 | 2h |
| **P6 backlog_later 并入** | 项目都标 🟡 / 🟠 进对应章节 | 低 | 1h |
| **P7 老文件清理** | 5 个老文件 git rm；commit "backlog: collapse 5 sources to capability framework" | 低 | 0.5h |
| **总计** | | | ~14-16h |

每个 Phase 一次 PR，可独立 review。

---

## 11. 决策点（确认后实施）

- **D-1** 顶部索引派生方式：手动 / 工具？— 推荐手动起步
- **D-2** ID 是否强制改新格式（CAP-NN-DOMAIN-SEQ）？— 推荐保留旧 ID，仅新建项用新格式
- **D-3** archive 文件按季度（2026-q2）还是按月？— 推荐季度
- **D-4** `docs/backlog.md` 路径是否保留？— 推荐保留（外部链接/工具不破坏）
- **D-5** WONTFIX 立即归档 vs 留 7 天？— 推荐立即（决定后无变更）
- **D-6** Phase P1 骨架是否包含示例 item（每能力 1 条样例）？— 推荐包含，便于 review 时确认 schema 可用

---

## 12. 不动的边界

以下文件**不**纳入本框架（保留独立轴）：
- `docs/plans/*` — 路线图 / PR 打包，主轴是时间和 PR，不是 capability
- `docs/reviews/*` — 审查 snapshot，不维护状态
- `docs/architecture/*` ADR — 决策记录
- `docs/product/roadmap/*` — 产品演进路径
- `journeys/status-board.yaml` — journey 验收状态机，独立工具链
- GitHub Issues — 仍可用作长期追踪入口；本 backlog 与 issue 单向引用（item 引 issue#，issue 不强制反向）

---

## 13. Review 检查项（确认时请逐条回答）

1. 14 capability 主轴 OK？还是要换 7 业务功能 / 6 交互模式？
2. 单文件 `backlog.md` 分章节 vs 14 个独立文件 — 走单文件？
3. Item schema 字段够不够？要加 / 减哪些？
4. 横切 cap-x-cross 这个出口是否清晰？还是该有更多顶层桶？
5. Archive 30 天滚动 OK？还是更短/更长？
6. 决策点 D-1~D-6 各选哪条？
7. 迁移阶段 P1-P7 顺序 OK？还是先吃 backlog.md（最重的）？

---

**满意后我从 P1（骨架）开始落地。**
