# DDD API 设计在 GoCell 分层中的适用范围

> 系列文档 1/4 · 配套文档：[002 能力盘点](./202605131500-002-framework-capability-inventory.md) · [003 交互模式](./202605131500-003-capability-interaction-patterns.md) · [004 缺口分析](./202605131500-004-capability-gap-analysis.md)

## 结论

DDD 核心建模（限界上下文 / 聚合 / 实体 / 领域服务 / 仓储 / 领域事件）**只适用于 `cells/`**。其他分层各有自己的设计语汇，强套 DDD 反而会引入伪领域抽象。

## 分层定性

| 分层 | DDD 适用性 | 主导设计语汇 |
|------|------------|--------------|
| **`cells/`** | 主战场 | Bounded Context = Cell；Slice = Use Case / 应用服务；Aggregate / Entity / Repository / Domain Event 都在 slice 内 |
| **`contracts/`** | 周边概念 | 是 DDD 的 **Published Language**，但不需要"再做 DDD 设计"——它本身是 schema-first 的边界协议（OpenAPI / JSON Schema / event payload），关注点是版本演化、向后兼容（见 `api-versioning.md`） |
| **`adapters/`** | 周边概念 | 是 DDD 的 **Anti-Corruption Layer** 物化（postgres / rabbitmq / oidc / vault 等），职责是"翻译外部协议到 kernel/runtime 接口"，禁止塞业务规则 |
| **`kernel/`** | 不适用 | 框架底座 / 元模型；对标 K8s + fx，设计语汇是 declarative resource + lifecycle + sealed registry，不是领域模型 |
| **`runtime/`** | 不适用 | 横切技术能力（http / auth / worker / observability / outbox）；设计语汇是中间件链 + 可组合 hook + readyz/metrics |
| **`pkg/`** | 不适用 | 纯库（errcode / httputil / redaction），无状态 utility |
| **`cmd/` `tools/`** | 不适用 | CLI / 治理工具，过程式脚本 |

## 关键边界

DDD 不是越多越好。GoCell 的分层规则明确隔离了"业务领域"与"框架能力"：

1. **kernel / runtime / adapters 中出现"领域服务""聚合"是 smell** — 这些层只承载技术能力。例如 `runtime/audit/ledger` 不是"审计聚合"，它是 hash chain 存储 primitive；真正的"审计领域"在 `cells/auditcore/`。
2. **contracts/ 的 DDD-ness 已经下沉为 governance 规则** — 例如 v1 schema 演化、`additionalProperties` 策略、event payload 版本化（见 `eventbus.md` §事件负载），不需要再叠 DDD 词汇。
3. **adapters/ 的 ACL 是承诺而非建模** — 通过"adapter 只实现 kernel/runtime 接口、不依赖 cells/"这条依赖规则强制（CLAUDE.md §依赖规则），不靠 DDD 文档约束。

## Cell 内部的 DDD-ish 结构（按 GoCell 词汇）

```
cells/<cell>/
├── cell.yaml                 # 元数据（id/type/consistencyLevel/owner/schema.primary/verify.smoke）
├── cell.go / cell_init.go    # Cell 实例 + Init(reg) 注册
├── cell_providers.go         # 选项 / 依赖装配
├── cell_gen.go               # codegen 派生（禁手改）
├── slices/<slice>/
│   ├── slice.yaml            # 元数据（contractUsages / verify.unit / verify.contract / allowedFiles）
│   ├── handler.go            # HTTP / event handler（薄层）
│   ├── service.go            # 应用服务（NewXxx(...) (*X, error)，TxRunner nil 检查）
│   └── *_test.go             # unit / contract / oracle / intent / outbox
├── internal/
│   ├── dto/                  # 跨 slice 数据载体
│   ├── ports/                # 仓储 / 外部依赖接口
│   ├── adapters/postgres/    # 仓储 PG 实现
│   ├── mem/                  # 仓储内存实现（test）
│   └── <domain>mint/         # 领域计算（如 sessionmint、ledgermint）
├── postgres/                 # cell 级 PG schema / migrations / driver wiring
└── mem/                      # cell 级 mem store
```

## DDD 适用性的设计风险

如果对非 `cells/` 层强行套 DDD：

- **kernel/ 里建"聚合"** → 与 K8s declarative model 冲突，破坏元模型一致性
- **runtime/ 中间件做"领域决策"** → 横切关注点泄漏到业务路径，违反 cell metric label `cell="_runtime"` 设计前提
- **adapter 里加"领域服务"** → 等于在 ACL 里塞业务规则，破坏分层依赖（adapters/ 不依赖 cells/）

## 一句话总结

**DDD 收敛到 `cells/`，其他层按各自的元模型 / 中间件 / ACL 语汇设计**，这正是 GoCell 分层依赖规则的初衷。
