# Go 编码规范

## 分层依赖

| 层 | 允许依赖 | 禁止依赖 |
|----|----------|----------|
| kernel | 标准库、pkg、yaml parser | runtime、adapters、cells |
| cells | kernel、runtime | adapters |
| runtime | kernel、pkg | cells、adapters |
| adapters | kernel、runtime | cells |
| pkg | 标准库 | kernel、cells、runtime、adapters |
| cellmodules | 所有层 | 无 |
| examples | 所有层 | 无 |

## DDD 分层

- handler：参数绑定、鉴权结果消费、响应返回。
- application：业务编排。
- domain：实体、值对象、领域服务，不依赖框架。
- repository：持久化，不放业务判断。

Entity 经 DTO converter 出 wire。跨聚合通过 EventBus 或 contract 解耦。

## 一致性级别

| 级别 | 语义 | 测试要求 |
|------|------|----------|
| L0 | 本地纯计算 | table-driven 单元测试 |
| L1 | 单 cell 本地事务 | 事务完整性测试 |
| L2 | 本地事务 + outbox | outbox 原子性 + consumer 幂等 |
| L3 | 跨 cell 最终一致 | replay + 投影重建 |
| L4 | 长延迟设备闭环 | 状态机、超时、重试、迟到消息 |

L2 覆盖由 `L2-OUTBOX-ATOMICITY-COVERAGE-01` 守卫。

## 工程护栏

- 函数认知复杂度 ≤ 15。
- 同义字符串重复三次及以上抽常量。
- no-op、fallback、空实现必须写业务理由。
- 必填 service 依赖用 `gocell:"required"` 生成 validate。
- `clock.Clock` 是位置参，构造器内用 `clock.MustHaveClock`。
- 禁止用 `WithClock` option 或 Config 字段传 clock。

## 命名

- DB 字段 snake_case。
- JSON、query、path、event header 字段 camelCase。
- 错误使用 `errcode`。
- mock 放同包测试文件。
- cell 单测不 import 平台 adapters；集成测试用 build tag 明确隔离。

## 覆盖率

- kernel ≥ 90%。
- 新增或修改代码 ≥ 80%。
- handler 用 httptest 覆盖参数校验、鉴权、错误码。

## 数据库迁移

- 已提交 migration 只增不改；例外必须有 ADR 说明。
- 新字段必须有默认值或允许 NULL。
- 大表索引用 `CREATE INDEX CONCURRENTLY`。
- 文件命名：`{序号}_{动词}_{对象}.sql`。

## 安全检查点

- 新端点加 JWT 或显式 `auth.Route{Public: true}`。
- `/internal/v1/` 必须声明 caller、鉴权和网络隔离。
- 列表接口强制分页，`limit` 上限 500。
- 生产配置禁止 localhost fallback 和 noop publisher。

## API

- 资源用复数名词，动作由 HTTP method 表达。
- 状态码：200 GET/PUT/PATCH，201 POST，202 async，204 DELETE。
- 列表响应：`data`、`nextCursor`、`hasMore`。
- 错误响应使用 shared error schema。
