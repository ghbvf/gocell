# ADR: Runtime event topic equals contract ID (no version suffix stripping)

**Date**: 2026-05-05
**Status**: Accepted
**PR**: #376

---

## Context

PR #376（B3 批次）在 `contractgen` 的 spec 渲染管线中引入了 `stripVersionSuffix` 逻辑：
将 `Topic` 字段从 `event.order-created.v1` 裁剪为 `event.order-created`，目的是让事件
topic 不携带版本后缀，与 broker exchange 的命名习惯对齐。

然而该修改导致如下断链：

1. **生成代码与 broker 配置漂移**：broker exchange 和 binding key 均按 contract ID 全名
   配置（已有 17+ 个生产 const 形如 `"event.session.created.v1"`）。stripping 后
   `spec_gen.go` 中的 `Topic` 与 broker 拓扑不一致，消费方无法收到消息。
2. **archtest 无法静态守护**：stripped topic 与 contract ID 之间无单射关系，
   `EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01` 等 archtest 门无法对比 Topic 与
   `contract.yaml` 中的 `id` 字段来验证一致性。
3. **测试失败**：`TestBuildContractSpec_Event_OrderCreated` / `TestRender_Golden` 等
   golden file 测试均断言 `Topic == ID`，stripping 导致这些测试全部失败。

## Decision

**删除 `stripVersionSuffix`。Topic 字段等于 contract ID，不做任何裁剪。**

具体规则：

- `spec_gen.go` 中的 `Topic` 字段值 == contract `id` 字段值（含版本后缀）。
- 例：contract `id: event.session.created.v1` → `Topic: "event.session.created.v1"`。
- broker exchange / routing key 的命名规范沿用现有惯例：全 ID 含版本。
- 如需不含版本的 exchange 名称，在 broker adapter 层（`adapters/rabbitmq`）做一次性派生，
  不在 contractgen 层裁剪。

## Consequences

**正面**：
- contract ID 是 Topic 的唯一真理源；archtest `EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01`
  可以直接 string 比较 `spec.Topic == contract.ID`，无需额外映射。
- 现有 17+ 个版本化 `const` 直接用作 Topic，无需迁移。
- 生成的 golden file 与实际 broker 配置保持一致，测试不再误报。

**负面**：
- broker exchange 名称含版本后缀，当同一 domain 事件升级为 v2 时需新建 exchange。
  这是预期成本——v1/v2 并行期间消费方可以分别绑定，完成迁移后下线 v1 exchange。
- 如果 broker 平台强制要求不含版本的 exchange 名，需在 adapter 层实现一次性转换规则
  （`adapters/rabbitmq.TopicFromContractID`），该工作登记在 backlog。

## Alternatives Considered

### 方案 A：保留 strip + 新增 `transportTopic` 字段

在 `contract.yaml` 增加可选 `transportTopic` 字段，默认等于裁剪后的值。

**被拒原因**：引入双源（`id` + `transportTopic`），FMT-18 需要同时校验两个字段，
schema 和治理规则复杂度翻倍，且所有现有 const 都要迁移到新字段。

### 方案 B：strip 仅在 adapter 层

contractgen 保持 `Topic == ID`，adapter 层（RabbitMQ）按需裁剪。

**结论**：这实际上就是本 ADR 的 Decision，只是明确说明裁剪在 adapter 层而非 contractgen。

## References

- Watermill: `message.Router.AddHandler` — handler key 用 full topic string（含版本）
- NATS JetStream: stream name 允许含 `.` 和版本后缀
- go-micro: topic 即 event type full name，含 major version
- PR #376 相关 failing tests: `TestBuildContractSpec_Event_OrderCreated`,
  `TestRender_Golden/event.order-created.v1/spec_gen.go`,
  `TestRender_Golden_Synth_Event/spec_gen.go`
