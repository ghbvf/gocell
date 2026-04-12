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
| [Kubernetes](https://github.com/kubernetes/kubernetes) | 110K | Apache 2.0 | Cell/Slice 声明模型、生命周期、校验链、编排 |

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

### cells/ — Cell 声明模型 + 生命周期

```
primary:   kubernetes/kubernetes → staging/src/k8s.io/api/core/v1/types.go（Pod 声明结构 → cell.yaml 参考）
secondary: kubernetes/kubernetes → pkg/kubelet/lifecycle/（生命周期钩子：Init → Ready → Running → Shutdown）
goal:      cell.yaml 声明式驱动，参考 Pod spec 的字段组织；生命周期参考 kubelet 但更轻量
```

### cells/*/slices/ — Slice 声明模型 + 校验

```
primary:   kubernetes/kubernetes → staging/src/k8s.io/api/core/v1/types.go（Container spec → slice.yaml 参考）
secondary: kubernetes/kubernetes → pkg/apis/core/validation/validation.go（字段校验模式）
goal:      slice.yaml 归属/职责/约束声明参考 Container spec；gocell validate 参考 k8s admission 校验链
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

## Go 标准库参考（问题修复用）

| 领域 | 标准库参考 | 关注点 |
|------|-----------|--------|
| 并发保护 | `sync`（Mutex / RWMutex / Once / WaitGroup / Map） | 锁粒度、Once 惯用法、copyChecker 模式 |
| 原子操作 | `sync/atomic` | Load/Store/CompareAndSwap 的正确用法 |
| Context 传播 | `context` | 取消传播、Value 的正确使用边界 |
| HTTP 处理 | `net/http`（Server / Handler / Transport） | Shutdown 优雅关闭、中间件链组合、超时设置 |
| 连接池 | `database/sql`（DB / Conn / Pool） | SetMaxOpenConns / SetConnMaxLifetime 策略 |
| IO 与资源释放 | `io`（Closer / Pipe / ReadAll） | defer Close 惯用法、Pipe 组合模式 |
| 错误处理 | `errors`（Is / As / Join / Unwrap） | 错误链设计、sentinel error vs 类型断言 |
| 密码学 | `crypto/*` | 常量时间比较、随机数生成 |
| 测试模式 | `testing`（T / B / TB） | Cleanup / Parallel / Helper 惯用法 |

## 组件官方库参考（问题修复用）

| GoCell 模块 | 官方库 | GitHub 路径 | 重点关注 |
|-------------|--------|-------------|---------|
| adapters/postgres | `jackc/pgx/v5` | `jackc/pgx` | 连接池、事务隔离、pgxpool 生命周期 |
| adapters/redis | `redis/go-redis/v9` | `redis/go-redis` | Pipeline/Tx、Pub/Sub 重连 |
| adapters/rabbitmq | `rabbitmq/amqp091-go` | `rabbitmq/amqp091-go` | Channel 不跨 goroutine、重连、Confirm |
| runtime/http | `go-chi/chi/v5` | `go-chi/chi` | 中间件顺序、RouteContext |
| runtime/auth/jwt | `golang-jwt/jwt/v5` | `golang-jwt/jwt` | SigningMethod、Claims、kid |
| adapters/oidc | `coreos/go-oidc/v3` | `coreos/go-oidc` | Provider 缓存、JWKS 刷新 |
| adapters/s3 | `aws/aws-sdk-go-v2` | `aws/aws-sdk-go-v2` | Retry、Context 超时 |
| adapters/websocket | `nhooyr.io/websocket` | `nhooyr.io/websocket` | 并发写保护、Close handshake |
| adapters/otel | `go.opentelemetry.io/otel` | `open-telemetry/opentelemetry-go` | TracerProvider 生命周期、Shutdown 顺序 |
| adapters/prometheus | `prometheus/client_golang` | `prometheus/client_golang` | Registry 隔离、Collector 注册时机 |
| DB migration | `pressly/goose/v3` | `pressly/goose` | 版本锁、并发 migration |
| 集成测试 | `testcontainers-go` | `testcontainers/testcontainers-go` | Container 生命周期、Cleanup |
| 文件监听 | `fsnotify/fsnotify` | `fsnotify/fsnotify` | Rename 语义、重复事件 |

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
