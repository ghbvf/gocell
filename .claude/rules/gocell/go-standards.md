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

| 层 | 允许依赖 | 禁止依赖 |
|----|---------|---------|
| kernel/ | 标准库 + pkg/ | runtime/ adapters/ cells/ |
| cells/ | kernel/ + runtime/ | adapters/(通过接口解耦) |
| runtime/ | kernel/ + pkg/ | cells/ adapters/ |
| adapters/ | kernel/ + runtime/(实现其接口) | cells/ |
| pkg/ | 标准库 | kernel/ cells/ runtime/ adapters/ |
| examples/ | 所有层 | - |

## DDD 分层(适用于 cells/ 和 examples/ 的应用层代码)

| 层 | 职责 | 禁止 |
|----|------|------|
| handler | 参数绑定 + 响应返回 | 业务判断 |
| application | 业务编排 | 领域规则 |
| domain | 聚合根/实体/值对象/领域服务 | 依赖框架 |
| repository | 数据持久化 | 业务逻辑 |

- Entity 禁止直接序列化为 API 响应,必须 DTO 转换
- 实体含行为方法(充血模型),状态变更走方法
- 跨聚合通过 EventBus 解耦,禁止直接调 Repository
- 接口定义在 domain/,实现在 repository/ 或 infrastructure/

## 一致性级别(L0-L4)

每次 CUD 操作必须标注一致性级别:

| 级别 | 含义 | 场景 | 机制 |
|------|------|------|------|
| L0 LocalOnly | 单 slice 内部本地处理 | 纯计算、校验 | 无需事务 |
| L1 LocalTx | 单 cell 本地事务 | session 创建、审计写入 | 单事务 |
| L2 OutboxFact | 本地事务 + outbox 发布 | session.created 事件 | transactional outbox |
| L3 WorkflowEventual | 跨 cell 最终一致 | 查询投影、合规追踪 | 事件消费 + 投影 |
| L4 DeviceLatent | 设备长延迟闭环 | 命令回执、证书续期 | 应用级状态机 |

- 禁止 L2 事件在写库后直接 eventbus.Publish
- 禁止 consumer unmarshal 失败时直接 return nil
- 每个新 consumer 必须声明: 幂等键、ACK 时机、失败重试策略

### 各级测试要求

| 级别 | 必须测试 |
|------|---------|
| L0 | 纯单元测试(输入/输出验证) |
| L1 | 事务完整性测试(testcontainers + 真实 DB) |
| L2 | outbox 原子性测试 + consumer 幂等测试 |
| L3 | event replay 测试 + 投影重建测试 |
| L4 | 状态机转换测试 + 超时/重试测试 + 延迟到达测试 |

## 工程质量护栏

- 函数认知复杂度上限 15,超过必须拆分
- 同义字符串重复 >= 3 次必须抽常量
- 空实现/no-op/fallback 必须写明业务原因
- 加密/签名/鉴权优先复用现有安全封装

## 命名规范

- DB 字段 snake_case,JSON 字段 camelCase,Query/Path 参数 camelCase
- 统一用 errcode 包返回错误,禁止裸 errors.New 对外暴露
- 错误必须包装上下文: fmt.Errorf("context: %w", err)

## 测试覆盖率

- kernel/ >= 90%(table-driven test)
- 新增/修改代码 >= 80%
- handler 层用 httptest,覆盖参数校验和错误码
- mock 定义在测试文件中（*_test.go 同包）
- 关键一致性测试禁止默认 t.Skip

## 数据库迁移

- up/down 对,禁止修改已有 migration
- 新字段必须有默认值或允许 NULL
- 大表索引用 CREATE INDEX CONCURRENTLY
- 命名: {序号}_{动词}_{对象}.sql

## 安全检查点

- 新端点必须加 JWT 中间件或在白名单中声明
- /internal/v1/ 必须声明调用方、鉴权方式、网络隔离边界
- 列表接口强制分页,pageSize <= 500
- 生产配置禁止 localhost 回退/noop publisher/静默降级

## 错误处理

- 禁止 errors.New 对外暴露,使用 errcode 包
- handler 层统一转换领域错误为 HTTP 状态码
- 500 不暴露内部细节,写 slog

## API 规范

- 资源命名复数名词,禁止动词 URL
- 使用正确状态码(200 GET/PUT/PATCH, 201 POST, 202 异步, 204 DELETE)
- 列表强制分页,pageSize 上限 500
- 统一响应格式: {"data": ..., "total": ..., "page": ..., "pageSize": ...}
- 统一错误格式: {"error": {"code": "ERR_...", "message": "...", "details": {...}}}

## GoCell 元数据文件规范

详见 docs/architecture/metadata-model-v3.md

### cell.yaml 必须字段

- id: Cell 唯一标识
- type: core / edge / support
- consistencyLevel: L0-L4
- owner: { team, role }
- schema.primary: 权威数据表声明
- verify.smoke: 冒烟验证命令
- l0Dependencies: L0 直接 import 依赖（条件字段，仅 L0 Cell）

### slice.yaml 必须字段

- id: Slice 唯一标识
- belongsToCell: 所属 Cell
- contractUsages: 契约使用声明（role 按 contract kind 选择: http→serve/call, event→publish/subscribe, command→handle/invoke, projection→provide/read）
- verify.unit: 单元测试命令
- verify.contract: 契约测试命令
- owner/consistencyLevel/allowedFiles: 缺省时继承 cell.yaml

### CLI 工具

- `gocell validate` - 验证 cell.yaml/slice.yaml 元数据合规
- `gocell scaffold cell` - 生成新 Cell 骨架
- `gocell scaffold slice` - 生成新 Slice 骨架
- `gocell generate assembly` - 从元数据生成 assembly
- `gocell check deps` - 检查分层依赖方向
- `gocell check contracts` - 检查契约兼容性
