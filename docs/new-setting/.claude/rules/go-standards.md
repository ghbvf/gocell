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

# Go 编码规范（跨层通用）

> 层内专属规则（依赖约束、覆盖率、DDD 分层、测试要求）见各层 CLAUDE.md。
> 本文件只保留所有层通用的编码规范。

## 工程质量护栏

- 函数认知复杂度上限 **15**，超过必须拆分
- 同义字符串重复 ≥ 3 次必须抽常量
- 空实现 / no-op / fallback 必须写明业务原因（注释说明）
- 构造函数出口保证所有字段非 nil——可选依赖在构造函数内 fallback（如 `outbox.DiscardPublisher{}`），禁止 nil 传播到方法调用处
- 加密/签名/鉴权优先复用现有安全封装，禁止自建

## 命名规范

- DB 字段 `snake_case`，JSON 字段 `camelCase`，Query/Path 参数 `camelCase`
- 统一用 `errcode` 包返回错误，禁止裸 `errors.New` 对外暴露
- 错误必须包装上下文：`fmt.Errorf("context: %w", err)`
- mock 定义在测试文件中（`*_test.go` 同包）

## 安全检查点

- 新端点必须加 JWT 中间件或在白名单中显式声明 `Public: true`
- `/internal/v1/` 必须声明调用方、鉴权方式、网络隔离边界
- 列表接口强制分页，`pageSize` 上限 **500**
- 生产配置禁止：`localhost` 回退 / noop publisher / 静默降级
