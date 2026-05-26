---
paths:
  - "kernel/**/*.go"
  - "cells/**/*.go"
  - "runtime/**/*.go"
  - "adapters/**/*.go"
  - "pkg/**/*.go"
  - "cmd/**/*.go"
  - "examples/**/*.go"
---

# Go 编码规范

## GoCell 分层依赖规则

| 层 | 允许依赖 | 严禁依赖 |
|----|---------|---------|
| kernel/ | 标准库 + pkg/ + gopkg.in/yaml.v3（metadata 解析） | runtime/ adapters/ cells/ |
| cells/ | kernel/ + runtime/ | adapters/（通过接口解耦） |
| runtime/ | kernel/ + pkg/ | cells/ adapters/ |
| adapters/ | kernel/ + runtime/（实现其接口） | cells/ |
| pkg/ | 标准库 | kernel/ cells/ runtime/ adapters/ |
| examples/ | 所有层 | — |

## DDD 分层（适用于 cells/ 和 examples/）

| 层 | 职责 | 范围外 |
|----|------|--------|
| handler | 参数绑定 + 响应返回 | 业务判断 |
| application | 业务编排 | 领域规则 |
| domain | 聚合根/实体/值对象/领域服务 | 依赖框架 |
| repository | 数据持久化 | 业务逻辑 |

- Entity 经 DTO 转换再序列化为 API 响应
- 实体含行为方法（充血模型），状态变更走方法
- 跨聚合通过 EventBus 解耦
- 接口定义在 domain/，实现在 repository/ 或 infrastructure/

## 一致性级别（L0-L4）

| 级别 | 含义 | 场景 | 机制 |
|------|------|------|------|
| L0 LocalOnly | 单 slice 内部本地处理 | 纯计算、校验 | 无需事务 |
| L1 LocalTx | 单 cell 本地事务 | session 创建、审计写入 | 单事务 |
| L2 OutboxFact | 本地事务 + outbox 发布 | session.created 事件 | transactional outbox |
| L3 WorkflowEventual | 跨 cell 最终一致 | 查询投影、合规追踪 | 事件消费 + 投影 |
| L4 DeviceLatent | 设备长延迟闭环 | 命令回执、证书续期 | 应用级状态机 |

### 各级测试要求

| 级别 | 必须测试 |
|------|---------|
| L0 | 纯单元测试（输入/输出验证） |
| L1 | 事务完整性测试（testcontainers + 真实 DB） |
| L2 | outbox 原子性测试 + consumer 幂等测试 |
| L3 | event replay 测试 + 投影重建测试 |
| L4 | 状态机转换测试 + 超时/重试测试 + 延迟到达测试 |

> L2 覆盖由 archtest `L2-OUTBOX-ATOMICITY-COVERAGE-01` Hard 强制（从 `consistencyLevel: L2` 枚举单元 + 按 Service 定义包名派生期望测试名 exact-name match，缺失即 CI 红）。subtype 分类（producer / hybrid / store-level）与各类测试位置/断言要求见 ADR `docs/architecture/202605241940-adr-l2-atomicity-subtypes.md`。

## 工程质量护栏

- 函数认知复杂度上限 15，超过必须拆分
- 同义字符串重复 ≥ 3 次抽常量
- 空实现/no-op/fallback 写明业务原因（注释）
- 构造函数出口必须经 `s.validateRequired()`（由 `gocell generate required-deps`
  从 Service struct 字段 `gocell:"required"` tag 生成）。可选依赖（logger /
  emitter）在构造函数内 fallback。clock 是**强制位置参** `clk clock.Clock`
  （首参，或 `ctx` 之后；漏传 = 编译错误，由 `CLOCK-POSITIONAL-INJECTION-01`
  form-lock 守正向形态），构造函数体内 `clock.MustHaveClock(clk, ...)` 保留为
  typed-nil panic 豁免（programmer error 语义，#883 Option A）——禁 `WithClock`
  option、禁输入 Config 的 `Clock` 字段（详见 ADR `202605270000`）。由
  `REQUIRED-DEP-NIL-GUARD-01` 三件套 archtest 静态守卫。OUTBOX-SERVICE-01
  SERVICE-02..05 sub-rule 持续守 outbox 侧约束
  （publish/import/DirectEmitter/WithOutboxWriter）。
- 加密/签名/鉴权优先复用现有安全封装

## 命名规范

- DB 字段 `snake_case`，JSON 字段 `camelCase`，Query/Path 参数 `camelCase`
- 错误用 `errcode.New(code, message)`，包装上下文用 `fmt.Errorf("context: %w", err)`
- mock 定义在测试文件中（`*_test.go` 同包）

## 测试覆盖率

- kernel/ ≥ 90%（table-driven test）
- 新增/修改代码 ≥ 80%
- handler 层用 httptest，覆盖参数校验和错误码

## 数据库迁移

- Migration 文件只增不改（已提交的 migration 不修改）
- 新字段必须有默认值或允许 NULL
- 大表索引用 `CREATE INDEX CONCURRENTLY`
- 命名：`{序号}_{动词}_{对象}.sql`

## 安全检查点

- 新端点加 JWT 中间件或在 `auth.Route{Public: true}` 白名单中显式声明
- `/internal/v1/` 必须声明调用方、鉴权方式、网络隔离边界
- 列表接口强制分页，`limit` 上限 500
- 生产配置使用真实 adapter（非 localhost 回退/noop publisher）

## API 规范

- 资源命名复数名词（users, sessions），操作用 HTTP method 表达
- 状态码：200 GET/PUT/PATCH | 201 POST | 202 异步 | 204 DELETE
- 统一列表响应：`{"data": [...], "nextCursor": "...", "hasMore": bool}`
- 统一错误响应：`{"error": {"code": "ERR_...", "message": "...", "details": [{"key": "...", "value": ...}]}}` （`details` 是 `array<{key,value}>`，5xx 强制空数组——共享 envelope `contracts/shared/errors/error-response-v1.schema.json` + ADR `docs/architecture/202605051730-adr-errcode-message-pii-safety.md`）
