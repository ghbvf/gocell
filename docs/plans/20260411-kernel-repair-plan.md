# Kernel 模块分阶段修复计划

> 日期: 2026-04-11（v3 — 对照 backlog 去重后修正）
> 基准: develop @ d9ace73

---

## 已完成汇总

PR#59-65 已修复 31 条 kernel findings，覆盖六角色深审全部 P1+P2 和大部分旧有 P2。

| PR | 批次 | 修复项 |
|---|---|---|
| #59 (C1 batch) | scaffold 安全 + assembly 守卫 + cell 生命周期 + 防御性拷贝 | ASJ-1~3, ASJ-5, CS-F5, R1B1-03/04, CS-AR-1, F-3, MR-R01, MR-R03, GOV-2 |
| #60 (C2 batch) | governance 语义 + metadata ownerCell + registry 拷贝 | GOV-1, GOV-3, MR-R02, MR-R05 |
| #61+#62 | verify 系统重写 + metadata 对齐 | CS-F1~F4 |
| #63 | audit-core L2 + httputil mapping | F-META-02, F-HTTP-MAP-01 |
| #64 | GOV-5 + GOV-6 | GOV-5 (VERIFY-05), GOV-6 (L0 targets) |
| #65 | strict YAML | F-META-01 |

> v1/v2 计划中 Wave 1 (A1-A4) 和 Wave 2 (B7/C0) 的 19 条项已全部在 PR#59-60 完成，无需再做。

---

## 真正剩余：10 条 kernel TODO

### 依赖拓扑

```
Wave A: Governance 规则补全（可直接开始）
  PR-GA: G-7                          auto-derive（MR-R01/R02 已修，数据已正确）
      ↓

  PR-GB: G-1 + G-4 + F-5(wiring)     FMT-11 + deprecated + journey ref 接入
         G-2 + G-6                    actor 一致性 + assembly boundary
      ↓ 依赖 GA（auto-derive 完成后规则才有意义）
      ↓ 5 条规则可在 1-2 PR 内完成

Wave B: Outbox/EventBus 重写（独立路径）
  PR-D1: 0-F (A3-01~07)              kernel 接口迁移
         + F-ID-01 + F-OB-03         + idempotency/outbox 收尾
      ↓

  PR-D2: 0-B2 (RL-01~08)  ‖  PR-D3: 0-D (S-01~06)

Wave C: 架构（设计先行）
  设计: CS-AR-2 + CS-AR-3 + F-OB-01
```

### 并行矩阵

```
时间 →  T1          T2          T3           T4
       ┌──────────┐
       │ PR-GA    │ G-7 auto-derive
       └────┬─────┘             ┌──────────┐
            │                   │ PR-D1    │ 0-F kernel 接口
       ┌────┴─────┐             └────┬─────┘
       │ PR-GB    │ G-1/2/4/5/6     │
       └──────────┘             ┌────┴─────┐┌──────────┐
                                │ PR-D2    ││ PR-D3    │
                                │ 0-B2     ││ 0-D      │
                                └──────────┘└──────────┘
```

> Wave A 和 Wave B 零交集，可全程并行

### PR 明细

#### Wave A: Governance 规则补全（~2d）

**PR-GA: 自动推导（~0.5d）**

| ID | 文件 | 问题 | C |
|---|---|---|---|
| G-7 | `metadata/parser.go` | belongsToCell/ownerCell 自动推导 | C2 |

> 前置 MR-R01/R02 已在 PR#59-60 修复。G-7 是增量功能，不是修 bug。

**PR-GB: 规则补全（~1.5d）**

| ID | 文件 | 问题 | C |
|---|---|---|---|
| G-1 | `governance/rules_fmt.go` | FMT-11 动态状态字段禁入非 status-board | C2 |
| G-2 | `governance/rules_topo.go` | TOPO-07 actor.maxConsistencyLevel 约束 | C1 |
| G-4 | `governance/rules_fmt.go` | deprecated contract 引用阻断 | C1 |
| G-6 | `governance/validate.go` | assembly boundary.yaml 存在性校验 | C1 |
| F-5 | `governance/validate.go` | journey ref 校验接入 Validate() — `Catalog.Validate()` 已有逻辑 | C1 |

#### Wave B: Outbox/EventBus 重写（~5d，独立路径）

| PR | 条目数 | 内容 | 依赖 |
|---|---|---|---|
| **D1** | 9 | 0-F kernel 接口 + F-ID-01 + F-OB-03 | 无 |
| **D2** | 8 | 0-B2 Outbox Relay 三阶段 | D1 |
| **D3** | 6 | 0-D RabbitMQ Solution B | D1 |

> D2 ‖ D3 可并行

#### Wave C: 架构（设计先行）

| ID | 问题 | 前置 |
|---|---|---|
| CS-AR-2 | Dependencies 接口精简 | D1 |
| CS-AR-3 | kernel/cell 去 net/http 依赖 | D1 |
| F-OB-01 | Writer 批量写 | D1 |

---

## 执行时间估算

| Wave | 条目 | PR 数 | 预估 | 可并行 |
|---|---|---|---|---|
| A Governance | 6 | 2 | 2d | GA→GB |
| B Outbox | 23 | 3 | 5d | D2‖D3 |
| C 架构 | 3 | — | 设计 | — |

关键路径 A: GA → GB（串行 2d）
关键路径 B: D1 → D2‖D3（串行 1.5d + 并行 3.5d）
A 和 B 全程并行，实际关键路径 = max(2d, 5d) = 5d
