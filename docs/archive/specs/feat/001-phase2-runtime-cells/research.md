# Research — Phase 2: Runtime + Built-in Cells

## 对标框架源码拉取清单

实施前必须按 CLAUDE.md 对标规则 WebFetch 以下源码。

### Wave 0: kernel/ 接口扩展

| 对标 | URL | 提取要点 |
|------|-----|---------|
| Watermill Subscriber | `https://raw.githubusercontent.com/ThreeDotsLabs/watermill/main/message/subscriber.go` | Subscriber 接口签名、Close 语义、context 传播 |
| Watermill Message | `https://raw.githubusercontent.com/ThreeDotsLabs/watermill/main/message/message.go` | Message struct、Ack/Nack 模式 |

### Wave 1: runtime/ 独立模块

| 模块 | Primary 对标 URL | 提取要点 |
|------|-----------------|---------|
| http/middleware | `https://raw.githubusercontent.com/go-kratos/kratos/main/middleware/middleware.go` | Handler chain 模式、context 传播 |
| http/middleware | `https://raw.githubusercontent.com/zeromicro/go-zero/master/rest/handler/recoverhandler.go` | Recovery 实现 |
| http/middleware | `https://raw.githubusercontent.com/zeromicro/go-zero/master/rest/handler/timeouthandler.go` | Timeout 模式 |
| config | `https://raw.githubusercontent.com/go-micro/go-micro/master/config/config.go` | Source + Watcher 接口 |
| config | `https://raw.githubusercontent.com/go-kratos/kratos/main/config/config.go` | Config 接口、Value 类型 |
| worker | `https://raw.githubusercontent.com/zeromicro/go-zero/master/core/service/servicegroup.go` | ServiceGroup 并发管理 |
| auth | `https://raw.githubusercontent.com/go-kratos/kratos/main/middleware/auth/auth.go` | Auth 中间件模式 |
| observability | `https://raw.githubusercontent.com/go-kratos/kratos/main/middleware/tracing/tracing.go` | Span 创建、context 传播 |
| observability | `https://raw.githubusercontent.com/go-kratos/kratos/main/middleware/metrics/metrics.go` | 指标注册模式 |
| eventbus | `https://raw.githubusercontent.com/ThreeDotsLabs/watermill/main/message/router.go` | Router/Handler 模式、重试 |

### Wave 2: bootstrap

| 对标 | URL | 提取要点 |
|------|-----|---------|
| Uber fx app | `https://raw.githubusercontent.com/uber-go/fx/master/app.go` | Lifecycle hooks、Start/Stop 编排、错误回滚 |
| Kratos app | `https://raw.githubusercontent.com/go-kratos/kratos/main/app.go` | App 接口、Option 模式、signal handling |

### Wave 3: Cell 生命周期

| 对标 | URL | 提取要点 |
|------|-----|---------|
| K8s Pod types | `https://raw.githubusercontent.com/kubernetes/kubernetes/master/staging/src/k8s.io/api/core/v1/types.go` | Pod/Container 生命周期、readiness/liveness |

## 技术决策记录

### JWT 库选型: golang-jwt/jwt/v5

- 成熟度: GitHub 6K+ stars, Go 社区标准
- RS256 + JWKS kid 轮换原生支持
- 无重量级传递依赖
- 替代方案: lestrrat-go/jwx（更全面但更重）— 否决

### 配置 Watcher: fsnotify

- OS 级文件通知，低延迟
- 替代方案: 定时轮询 — 否决（延迟高、CPU 浪费）

### HTTP 路由: go-chi/chi/v5

- `net/http` 兼容，标准 middleware 签名
- 替代方案: gorilla/mux（停维）、gin（非标准签名）— 否决

### In-memory EventBus 设计

- 实现 kernel/outbox.Publisher + outbox.Subscriber
- 基于 Go channel + goroutine per subscriber
- 重试: 指数退避 (100ms, 200ms, 400ms)，3 次后 dead letter
- Dead letter: 内存 slice，暴露 Len() 和 Drain() 用于可观测性
- 线程安全: sync.RWMutex 保护订阅注册
