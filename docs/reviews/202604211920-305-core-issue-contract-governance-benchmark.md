# 核心问题对标报告 04：契约治理单一事实源（L6 + CONTRACT-BREAKING-01）

## 问题定义

- 对应 backlog：`L6 CONTRACTTEST-MODEL-ALIGN-01`、`CONTRACT-BREAKING-01`、`CONTRACT-CODEGEN-01`
- 当前风险：同一 contract 在 metadata 与 contracttest 两条链路语义不一致，破坏性变更缺系统性阻断

---

## 上游对标（3 项）

| 项目 | 证据 | 观察到的模式 | 对 GoCell 的启示 |
|---|---|---|---|
| Buf | breaking rules 文档与 CLI | 分级破坏性规则 + against 基线比较 + CI 可机器消费输出 | 建立 `gocell check contract-breaking`，支持规则分层与 main 基线比较 |
| Kubernetes API/CRD | CRD validation、deprecation policy | 版本并存、弃用窗口、迁移前不删旧版本 | contract 生命周期增加“并存-迁移-下线”显式阶段 |
| oapi-codegen / goa | strict server、DSL/codegen | 单一事实源驱动代码生成，编译期守卫避免接口漂移 | 强化 contract -> typed DTO/stub 生成链，减少手工双写 |

---

## 结论（带权衡）

- 可落地的共识模式：
  1. contract 只能有一个权威模型（共享类型）
  2. 破坏性变更在 CI 阶段阻断，而不是运行期发现
  3. 版本弃用必须有窗口与迁移条件
- 与上游差异：
  - Buf/OpenAPI 偏标准 schema（proto/openapi），GoCell 为 YAML 合同，需要自建规则引擎

---

## 建议落地方案

1. 新增 `pkg/contracts/schema_types.go`，metadata 与 contracttest 统一引用
2. 增加 `gocell check contract-breaking --against <base>`
3. 规则初始集建议包含：字段删除、字段类型变更、必填收紧、path/method 改动
4. 为 `lifecycle: deprecated` 增加“最短保留窗口 + 消费方迁移完成”检查

---

## 与当前代码映射

- 双轨模型：`pkg/contracttest/contracttest.go:91` vs `kernel/metadata/types.go:158`
- 目标：契约语义一致、演进可治理、变更可阻断
