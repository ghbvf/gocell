# GoCell 框架对标参考索引

本文记录 GoCell 开发过程中的对标框架，用于 review 对比、问题解决和 AI agent 开发参考。

## 对标框架

| 框架 | Stars | 许可证 | 主要参考维度 |
|------|-------|--------|------------|
| [Uber fx](https://github.com/uber-go/fx) | 5K | MIT | Cell 生命周期、Module 注册、依赖注入 |
| [go-zero](https://github.com/zeromicro/go-zero) | 35K | MIT | 代码生成（goctl）、限流熔断、Worker/ServiceGroup |
| [Kratos](https://github.com/go-kratos/kratos) | 22K | MIT | 中间件链、错误模型、gRPC/HTTP 双协议、transport 抽象 |
| [go-micro](https://github.com/micro/go-micro) | 23K | Apache 2.0 | Config 热更新、服务注册、Auth 模块、PubSub |
| [Watermill](https://github.com/ThreeDotsLabs/watermill) | 6K | MIT | 事件驱动 Pub/Sub、多后端 adapter、CQRS |

## 按 GoCell 模块的参考映射

### kernel/cell.go — Cell 运行时

```
primary:   uber-go/fx          → fx/module.go, fx/lifecycle.go, fx/app.go
secondary: go-kratos/kratos    → app.go, transport/
goal:      比 fx 更显式（cell.yaml 声明 vs 反射发现），必须有 Health/Ready
```

### kernel/generator/ — Assembly 代码生成

```
primary:   zeromicro/go-zero   → tools/goctl/（模板引擎、目录生成）
secondary: go-kratos/kratos    → cmd/kratos/（项目初始化、proto 生成）
goal:      从 cell.yaml/assembly.yaml 生成 main.go，不是从 proto/API 定义
```

### kernel/scaffold/ — 脚手架

```
primary:   zeromicro/go-zero   → tools/goctl/api/, tools/goctl/rpc/
secondary: go-kratos/kratos    → cmd/kratos/internal/project/
goal:      new-cell / new-slice / new-contract，生成目录 + metadata + 测试骨架
```

### runtime/http/middleware/ — 中间件链

```
primary:   go-kratos/kratos    → middleware/（链式组合、transport 抽象）
secondary: zeromicro/go-zero   → rest/handler/（内置中间件集）
goal:      chi-based，但参考 Kratos 的 middleware.Handler 签名设计
```

### runtime/config/ — 配置加载 + 热更新

```
primary:   micro/go-micro      → config/（Source 多后端、Watch 热更新）
secondary: go-kratos/kratos    → config/（file/env/flag 多源、Watcher）
goal:      config-core Cell 通过 outbox 事件推送变更，不是轮询
```

### kernel/outbox/ + kernel/idempotency/ — 事件驱动

```
primary:   ThreeDotsLabs/watermill → message/（Message 统一模型、Publisher/Subscriber 接口）
secondary: micro/go-micro          → events/（Event 模型、Store/Stream 抽象）
goal:      outbox 模式 + 幂等消费，不是直接 publish
```

### pkg/errcode/ — 错误模型

```
primary:   go-kratos/kratos    → errors/（code + reason + message + metadata）
secondary: zeromicro/go-zero   → core/errorx/
goal:      保持现有 errcode 包风格，参考 Kratos 的 GRPCStatus() 转换
```

### runtime/worker/ + runtime/scheduler/ — 后台任务

```
primary:   zeromicro/go-zero   → core/service/servicegroup.go（多 goroutine 管理）
secondary: zeromicro/go-zero   → core/timex/（TimingWheel 时间轮）
goal:      统一管理 outbox relay / key rotation / cron / 后台 worker
```

### runtime/auth/jwt/ — JWT 验证

```
primary:   micro/go-micro      → auth/（JWT + Rules + Account 模型）
secondary: go-kratos/kratos    → middleware/auth/（JWT middleware）
goal:      RS256 钉扎、kid 轮换支持、Claims 注入 context
```

### adapters/ — 多后端适配

```
primary:   ThreeDotsLabs/watermill → 12+ backend adapters 的接口抽象模式
secondary: micro/go-micro          → 多 Store/Broker/Registry 后端
goal:      First-class（PG/Redis/OIDC）+ Family（RabbitMQ/WebSocket）+ Optional 三层
```

## Review 对比模板

每完成一个模块，输出对比文档：

```markdown
# Compare: GoCell {module} vs {reference} {module}

## 接口设计
- GoCell: ...
- Reference: ...
- 判断: GoCell 更好/更差/不同取舍

## 功能覆盖
- GoCell 有但 Reference 没有: ...
- Reference 有但 GoCell 没有: ...
- 是否需要补齐: ...

## 实现质量
- 代码行数对比
- 测试覆盖对比
- 文档完整度对比
```
