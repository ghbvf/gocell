# Backlog 目录说明

本目录配合根目录 `docs/backlog.md` 工作。

## 文件分工

| 文件 | 角色 |
|---|---|
| `docs/backlog.md` | **单源主入口**，按 14 capability units + 1 横切桶（`cap-x-cross`）分章节，含顶部索引 + Triggered Index |
| `docs/backlog/README.md` | 本文件 — 目录说明 + 框架引用 |
| `docs/backlog/archive/` | DONE / WONTFIX item 人工归档目录（按季度命名，如 `2026-q2-completed.md`） |

## 框架文档

- 设计稿：`docs/plans/202605070330-031-backlog-capability-framework.md`
- 主轴权威源：`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md` §1（14 capability units）

## 章节命名

每个 capability 章节用 `cap-NN-<short-name>` 锚点：

| # | Capability | 锚点 |
|---|---|---|
| 1 | Cell 声明与生命周期 | `cap-01-cell-lifecycle` |
| 2 | 元数据解析与治理 | `cap-02-metadata-governance` |
| 3 | Contract 注册与发现 | `cap-03-contract-registry` |
| 4 | HTTP 入站处理 | `cap-04-http-inbound` |
| 5 | 身份认证 (Authn) | `cap-05-authn` |
| 6 | 授权决策 (Authz) | `cap-06-authz` |
| 7 | 事务性事件发布 (Outbox Producer) | `cap-07-outbox-producer` |
| 8 | 异步事件消费 (Subscriber+Claimer) | `cap-08-subscriber-claimer` |
| 9 | 配置加载与热更新 | `cap-09-config-watcher` |
| 10 | 持久化与加密 | `cap-10-persistence-crypto` |
| 11 | 分布式锁 | `cap-11-distlock` |
| 12 | 启停编排 (Bootstrap) | `cap-12-bootstrap` |
| 13 | 可观测性 | `cap-13-observability` |
| 14 | 代码生成与治理工具链 | `cap-14-codegen-tooling` |
| X | 横切（CI/lint、跨能力重构、文档、发布） | `cap-x-cross` |

## Item schema（速查）

```
### [STATE] ID — 标题一句话

| Field | Value |
|---|---|
| Capability     | cap-NN-xxx (primary) — 可选 Also: [cap-MM, cap-PP] |
| Type           | feat / bug / debt / refactor / arch-opt / doc / test / fu |
| Priority       | P0 / P1 / P2 / P3 |
| Complexity     | Cx1 / Cx2 / Cx3 |
| ReleaseBlocker | yes / no（yes 必须配 Flag=🔴）|
| Flag           | 🔴 硬约束 / 🟠 条件延后 / 🟡 可延后 / 🟢 已纳入 plan |
| Trigger        | （仅 🟠 必填）触发条件 |
| Files          | ≤ 3 个 |
| Source         | PR# / review 报告路径 |

**现状**: ...
**修复方向**: ...
```

字段约束、cross-domain 决策规则、归档策略详见框架设计稿。

## State 规约

- `STATE` ∈ {OPEN, IN_PROGRESS, DONE, WONTFIX}（章节内按子段分组）
- DONE 留主文件直至人工迁移到 `archive/`
- WONTFIX 立即移到 `archive/` + 理由必填

## 迁移进度

| Phase | 状态 |
|---|---|
| P1 骨架 | ⏳ 进行中 |
| P2 backlog1 迁移 | ⬜ 未开始 |
| P3 backlog2 迁移 | ⬜ 未开始 |
| P4 backlog.md 拆分 | ⬜ 未开始 |
| P5 tech-debt-registry 并入 | ⬜ 未开始 |
| P6 backlog_later 并入 | ⬜ 未开始 |
| P7 一致性扫尾 | ⬜ 未开始 |
