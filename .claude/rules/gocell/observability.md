# 可观测性规范

## 日志

| Level | 使用场景 |
|-------|----------|
| Error | 正确性、安全或持久化失败 |
| Warn | 降级运行、重试预算耗尽 |
| Info | 生命周期、迁移、consumer 加入 |
| Debug | 本地诊断，生产默认关闭 |

禁止 Debug dump 完整请求、响应或 payload。错误日志必须带结构化关联字段。

## Redaction

errcode 的 Message、Public Details、Internal Details 三层分工见
`docs/architecture/202605051730-adr-errcode-message-pii-safety.md`。

trace span 和 slog sink 都必须 fail-closed redaction：

- span error 统一走 `pkg/redaction.RedactError`。
- span string attribute 先按 key 判敏感，再做 free-form scrub。
- slog sink 对敏感 attr 做统一清洗。
- last_error 持久化走同一 redaction 包。

没有业务 opt-out。需要原始诊断时走受控服务端日志，不写入 trace 或 wire。

## Readyz probe

- 依赖可用性 probe 用 `_ready` 后缀。
- 运行时操作 probe 不带 `_ready`。
- probe 名是运维契约，改名必须同步 docs/ops、dashboard、alert。
- cell repo readiness 由 cell 边界显式注册，禁止静默吞掉缺失 repo。

## Metrics cell label

HTTP 与 gRPC metrics 的 `cell` label 必须来自 closed set。合法值是 assembly
声明的 cell 集合；缺失、未知、越界归 `_runtime` 或 fail-fast，具体由 sealed resolver
定义。禁止业务代码手写裸 string label。

gRPC unary 和 stream interceptor 顺序必须保证 cell attribution 在 metrics/access log
之前完成。

## Redis namespace

Redis key namespace 使用 owner 维度表达：cell、role、resource。禁止把 service token、
outbox、projection 等跨域 key 混入 `_runtime` 前缀而丢失所有权。

## Readyz verbose

verbose readyz 输出分四通道：wire 响应、server log、trace、metrics。wire 必须裁剪敏感
error；server log 是主诊断通道；trace 默认跳过 health endpoint。

## Outbox envelope

trace、correlation、principal、occurred_at 等 envelope 字段由 `outbox.NewEntry` 和
sealed option 注入。业务不得通过 metadata 伪造 reserved key。

## Reconcile / idempotency / adapter metrics

metric label 值集必须冻结或经 typed enum 入口。新增 label value 同步更新 schema、
tests 和 docs/ops。高 cardinality 输入不能直接进入 label。

## Audit

audit payload 中的 replayable PII 必须 hash 或 redaction。trace 反查复用 auditquery
标准分页入口，不新增后门 endpoint。审计字段写入位置由 archtest 守卫，规则文件只保留约束摘要。
