# ADR · RabbitMQ runtime permanent classification（soft state, self-healing）

**Date**: 2026-05-05
**Status**: Accepted
**Supersedes**: 部分撤销 PR#173 引入的「runtime reconnect 不分类永久错误」语义；同时撤销同 PR 早期草案中的 hard-terminal 设计（StateTerminal phase + terminalCh + fatal-channel backlog 项）

## Context

post-PR#173（A.1 设计）`adapters/rabbitmq.Connection` 在运行时 reconnect 阶段不再调用 `isPermanentDialError`：所有 dial 失败（含 broker `Recover=false` 协议级永久错误）都进入无限退避重试。

**生产实测痛点（B2-A-02 / 027#4 / P0）**：admin 撤销 RMQ 凭证 / 删除 vhost 时，broker 关掉已建立的连接；adapter reconnect 收到 `*amqp.Error{Code:403}` 或 `amqp.ErrCredentials` 时**永远 retry**，`Health()` 始终返回 `errHealthReconnecting`，运维诊断只能靠「日志噪音 + 连不上 broker 的下游卡死现象」推断 → 必须人工介入。

本 PR 早期草案（hard terminal）尝试用 `StateTerminal` phase + `terminalCh` + `return false` 让 reconnect goroutine 退出，依赖 k8s livenessProbe 重启 pod。但 `docs/ops/readyz.md:12` 设计契约把 `/healthz` 钉在「process-level liveness 永远 200」，readinessProbe 失败仅撤 endpoint 不重启 pod，所以 hard terminal **既不重启也不自愈**——一旦命中，运维即使修复凭证也必须手动 restart pod。这是半残废终态，违反「彻底」原则。

## Decision

**采用 soft permanent classification + 持续 retry + 自愈**，对齐 `ThreeDotsLabs/watermill-amqp pkg/amqp/connection.go` 上游 reconnect 范式（无界 retry，仅 close 时退出）。

### 1. 删除 hard-terminal 整套脚手架

- 删 `StateTerminal` phase 枚举值 + `String()` / `connectionStateMessage` 分支
- 删 `Connection.terminalCh` 字段 + 所有 `make(chan struct{})` init / `close()` 调用
- 删 `WaitConnected` 中 `case <-terminalCh:` select 分支
- 删 `Health()` 中 StateTerminal phase 分支（`permanentErr != nil` 早于 phase switch 已覆盖）
- 删 backlog2 `RMQ-FATAL-SHUTDOWN-SIGNAL` 条目（不再需要 fatal-error channel 架构变更）

### 2. Soft permanent classification

`reconnectWithBackoff` 在 dial 失败分支调用 `classifyDialError`：

- `permanentClassDefinitive`（broker-emitted Server=true && !Recover / 协议层 sentinel / URI parse / TLS）→ 单次命中调 `markPermanent` → 设 `permanentErr` + 唤醒 WaitConnected 等待者 → **`continue` 不退 loop**
- `permanentClassInferred`（`amqp.ErrCredentials` / `amqp.ErrVhost`，amqp091-go 从 socket close 推断而非 broker close 帧）→ 累加 `pendingPermanentHits`，达到 `runtimePermanentConfirmHits=2` 才 `markPermanent` → 防止单次网络抖动 mid-handshake 误标
- `permanentClassNone`（recoverable / unknown）→ 重置 `pendingPermanentHits = 0`，记录 `lastError`，retry

dial 成功 → `markRecovered` → 清 `permanentErr` + `pendingPermanentHits` → reconnectLoop close `connected` channel → `/readyz` 自愈到 200。

### 3. 永久错误分类双层（互不重叠，必须并存）

- **第 1 层 — amqp091-go 包级 sentinel `errors.Is`**：
  - **inferred**：`amqp.ErrCredentials` / `amqp.ErrVhost` —— amqp091-go connection.go:1039-1043 注释明说「we know it's an auth error, but the socket was closed instead. Return a meaningful error.」是推断结果，网络瞬断同样命中，故需 N=2 确认
  - **definitive**：`amqp.ErrSASL` / `amqp.ErrSyntax` / `amqp.ErrFrame` / `amqp.ErrCommandInvalid` / `amqp.ErrUnexpectedFrame` —— amqp091-go 自分类协议级硬错，单次命中
  - sentinel 检查必须**早于**结构判定（singletons `Server=false` 默认，否则 `errors.As` 命中后 Server-gate 会错判 transient）
- **第 2 层 — `*amqp.Error.Server=true && Recover=false`**：broker 主动发的 connection.close 帧（amqp091-go `newError` 显式 `Server:true`），如 broker 重启 / 资源不存在的 403/404/530。`Server=false` 排除 amqp091-go 本地合成 mid-handshake TCP reset（`*amqp.Error{Code:501}`）—— transport 层 transient，broker 重启竞态产生
- **第 3 层 — `*url.Error`**：URI 解析失败（结构性永久）。**必须早于** `net.Error` 判定 —— `*url.Error` 通过嵌入式 `Timeout`/`Temporary` 实现 `net.Error` 接口，反序会被错判为 recoverable
- **第 4 层 — `net.Error`**：网络超时 / refused / DNS → recoverable
- **第 5 层 — 字符串 fallback**：amqp091-go 无 typed shape 的 plain error（`AMQP scheme` / `AMQP URI` / `invalid port` / `auth mechanism` / `x509:` / `tls: `）

### 4. 彻底清理 A.1 后的 dead code

- 删 `Config.MaxReconnectAttempts` 字段（注释明说 ignored）
- 删 `ErrAdapterAMQPReconnectExhausted` errcode 常量（runtime / startup 都不触发）
- 简化 `isTerminalConnectionError` 只检 `ErrAdapterAMQPConnectPermanent`
- 删 5 处 `_test.go` A.1 NOTE 注释 + ReconnectExhausted 相关测试
- 同步 `Health()`/`WaitConnected` godoc + `docs/guides/adapter-config-reference.md`（含 migration note）

### 5. 凭证脱敏 fail-closed

`sanitizeURL` 丢弃 `RawQuery` + `Fragment`（不仅 userinfo）—— RabbitMQ URI query 参数支持 password / TLS 文件路径（`https://www.rabbitmq.com/docs/uri-query-parameters`），白名单维护成本高且会随 upstream 漂移。代价是少量诊断信息（heartbeat / connection_timeout 之类无敏感参数也被丢），但对齐 fail-closed 原则。

## Consequences

### Positive

- **凭证撤销即时可观测 + 自愈**：`/readyz` 状态码立即翻 503（撤 endpoint），运维修复凭证后下一次 dial 成功即自愈到 200，无需人工 restart pod
- **goroutine 不泄漏 + 不卡死**：reconnect 持续运行；`closeCh` 仍是唯一退出路径
- **API 表面收紧**：`ConnectionPhase` 少 1 enum / `Connection` 少 1 chan field / errcode 少 1 常量 / `isTerminalConnectionError` 单一职责
- **与 watermill-amqp 上游对齐**：「持续 retry，状态暴露给上层」是同范式
- **吸收 030 plan A-06** `RMQ-WAITCONNECTED-DOC-FIX`，单 PR 闭口同源问题

### Negative / Trade-offs

- **不解决 pod kill**：本 PR 不让 pod 自动重启。语义改为「连不上就撤流」+「修好就接流」。如果运维场景必须 pod kill（例如 secret rotation 流程要求 pod restart），需要单独方案（K8s 外部 controller / 依赖 readyz 的 livenessProbe 配置）
- **/readyz 暴露形态**：默认 `/readyz` 只回 status code（200/503），不暴露 `rabbitmq_ready` 详情；详情仅在 `/readyz?verbose=true` 带 `X-Readyz-Token` 时可见（参见 `docs/ops/readyz.md`）。运维仪表盘想看 sentinel 文本必须接 verbose 端点
- **501 mid-handshake reset 不触发 markPermanent**：依赖 `Server` 字段区分 broker-classified vs 本地合成；amqp091-go 库行为若未来变化会破坏区分。属可接受技术债
- **inferred sentinel N=2 确认延迟**：真实 ErrCredentials/ErrVhost 场景下 readyz 翻 503 比 definitive 慢一个 backoff 周期（~30s 上限），换取假阳保护

## References

- `rabbitmq/amqp091-go types.go:50-77` — sentinel definitions (ErrSASL/ErrCredentials/ErrVhost/ErrSyntax/ErrFrame/ErrCommandInvalid/ErrUnexpectedFrame)
- `rabbitmq/amqp091-go connection.go:1007 / 1043 / 1096` — sentinel return points + amqp091-go inferred-from-socket-close 注释
- `ThreeDotsLabs/watermill-amqp pkg/amqp/connection.go` reconnect — 范式来源（无界 retry, 状态暴露）
- `docs/ops/readyz.md` §"What each endpoint returns" — `/healthz` 进程级 liveness 200 / `/readyz` status-code-first，verbose 端点带 token
- `https://www.rabbitmq.com/docs/uri-query-parameters` — RabbitMQ URI query 含敏感参数（驱动 sanitizeURL fail-closed）
- `docs/plans/202605011500-029-master-roadmap.md` 第 102 行 A4 PR-V1-RMQ-TERMINAL
- `docs/plans/202605051600-030-review-0504-implementation.md` A-06 RMQ-WAITCONNECTED-DOC-FIX（已吸收）
- `docs/reviews/20260504-systems-layer-03-adapters.md` §[P2]（已吸收）
