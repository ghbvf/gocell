# ADR · RabbitMQ runtime reconnect 永久错误分类（partial-revert PR#173 A.1）

**Date**: 2026-05-05
**Status**: Accepted
**Supersedes**: 部分撤销 PR#173 引入的「runtime reconnect 不分类永久错误」语义

## Context

post-PR#173（A.1 设计）`adapters/rabbitmq.Connection` 在运行时 reconnect 阶段不再调用 `isPermanentDialError`：所有 dial 失败（含 broker `Recover=false` 协议级永久错误）都进入无限退避重试。该决策的初衷：

- 启动期 `NewConnection` 已对永久错误 fail-fast，运维侧能立即定位
- 一旦 connection 建立过一次，「假定运维侧可恢复」，依赖 k8s readinessProbe（`/readyz` 失败 → SLO 时间窗口内剔除 endpoint）
- 与 `ThreeDotsLabs/watermill-amqp ConnectionWrapper.reconnect` 等 upstream 模式一致（无界 retry，仅 close 时退出）

**生产实测痛点（B2-A-02 / 027#4 / P0）**：

- admin 撤销 RMQ 凭证 / 删除 vhost 时，broker 主动 close 已建立的连接；adapter 重连时收到 `*amqp.Error{Code:403, Recover:false}`
- A.1 语义下：永远 retry → `Health()` 始终返回 `errHealthReconnecting` → `/readyz` 返回 503，但 k8s `livenessProbe` 仍永远 200（按 `docs/ops/readyz.md:12` 设计契约 `/healthz` 是进程级 liveness，不暴露依赖状态）
- 结果：pod **不重启**、不退出 endpoint pool（已在 readinessProbe 失败时撤出，但 pod 保持 Running 状态）→ 占用资源、日志噪音、运维需手工介入

## Decision

1. **运行时 reconnect 重新引入永久错误分类**：`reconnectWithBackoff` 在 `connect()` 失败分支调用 `isPermanentDialError(unwrappedErr)`，命中则：
   - 设置 `state=StateTerminal`、`permanentErr=errcode.Wrap(KindInternal, ErrAdapterAMQPConnectPermanent, ...)`
   - `close(terminalCh)`
   - 返回 false（reconnectLoop 退出，goroutine 不泄漏）
2. **永久错误判定收紧到 `Server=true && Recover=false`**：仅 broker-emitted 协议级拒绝才算永久（403/404/530），排除 amqp091-go 本地合成的 `*amqp.Error{Code:501, Server=false, Recover=false}`（mid-handshake TCP reset，broker 重启竞态产生）。对应 `TestConnection_ReconnectWithBackoff_TransientError_ContinuesIndefinitely` 的语义保留。
3. **彻底清理 A.1 后的 dead code**：
   - 删 `Config.MaxReconnectAttempts` 字段（注释明说 ignored）
   - 删 `ErrAdapterAMQPReconnectExhausted` errcode 常量（runtime / startup 路径都不会触发）
   - 简化 `isTerminalConnectionError` 只检 `ErrAdapterAMQPConnectPermanent`
   - 删 5 处 `_test.go` 中的 A.1 NOTE 注释 + 相关 ReconnectExhausted 测试
   - 同步 `Health()`/`WaitConnected` godoc + `docs/guides/adapter-config-reference.md`
4. **首次永久错误立即 Terminal**，不引入「连续 N 次」窗口阈值：broker `Recover=false` 已 authoritative，与 startup 路径对称。

## Consequences

### Positive

- **撤销凭证立即可观测**：`/readyz` 返回 503 + body 含 `rabbitmq_ready: rabbitmq: runtime reconnect failed (permanent)`，运维诊断时间从「等 SLO 后人工介入」缩短到下一次探针周期
- **API 表面收紧**：`Config` 少 1 字段、errcode 少 1 常量、`isTerminalConnectionError` 单一职责
- **goroutine 不泄漏**：runtime permanent 后 reconnectLoop 主动退出
- **吸收 030 plan A-06** `RMQ-WAITCONNECTED-DOC-FIX`，单 PR 闭口同源问题

### Negative / Trade-offs

- **不解决 pod kill**：`/readyz` 仅控制 readinessProbe（撤 endpoint），不触发 livenessProbe（`/healthz` 永远 200 是 readyz.md 设计契约）。Pod kill 需运维侧把 livenessProbe 接 `/readyz`（违反 readyz.md 设计），或新增 fatal-error channel 让 adapter 主动信号 bootstrap shutdown（架构变更级）。
   - **显式 carve-out 已登记 backlog**：`docs/backlog2.md` `RMQ-FATAL-SHUTDOWN-SIGNAL`（D 轨道）
- **501 mid-handshake reset 不触发 Terminal**：依赖 `Server` 字段区分 broker-classified vs 本地合成；amqp091-go 库行为变化（如未来移除 Server 字段语义）会破坏区分。属可接受技术债。

## References

- `rabbitmq/amqp091-go connection.go` — `*amqp.Error.Server` / `Recover` 字段语义
- `ThreeDotsLabs/watermill-amqp ConnectionWrapper.reconnect` — transient 无界 retry 范式（保留）
- `docs/ops/readyz.md` §"What each endpoint returns" — `/healthz` 进程级 liveness、`/readyz` 聚合 readiness 设计契约
- `docs/plans/202605011500-029-master-roadmap.md` 第 102 行 A4 PR-V1-RMQ-TERMINAL
- `docs/plans/202605051600-030-review-0504-implementation.md` A-06 RMQ-WAITCONNECTED-DOC-FIX（已吸收）
- `docs/reviews/20260504-systems-layer-03-adapters.md` §[P2]（已吸收）
