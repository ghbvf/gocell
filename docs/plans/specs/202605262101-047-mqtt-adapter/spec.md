# Feature Spec — MQTT Adapter (V11-4)

| 字段 | 值 |
|---|---|
| Roadmap ID | V11-4 / PR-V11-MQTT-ADAPTER |
| Spec ID | 047-mqtt-adapter |
| 起因 | `docs/product/roadmap/202605042330-001-v1.1能力路线-零信任mdm基础.md` §2 V11-4 |
| 优先级 | v1.1 P1 |
| 范围 | Adapter + examples/iotdevice 端到端 MQTT 联通 demo |
| 状态 | Spec / Plan 待批准 |

---

## 1. 问题陈述

GoCell 目前消息层只有 RabbitMQ（AMQP 0-9-1）一个 broker adapter。
MDM 推送通道、IoT 标准设备通道、零信任端点心跳都需要 MQTT。
example `iotdevice` 当前用 HTTP + WebSocket 走 L4 命令下发，
偏离 IoT 行业标准，未来 MDM 商业化部署阻塞。

## 2. 目标

1. 在 `adapters/mqtt/` 提供与 `adapters/rabbitmq/` **接口平行**的 MQTT 客户端 adapter，
   实现 `kernel/outbox.Publisher` 与 `kernel/outbox.Subscriber` 两个公开接口。
2. 支持 MQTT v5 协议（autopaho 客户端），覆盖 reconnect / QoS 1 / session expiry / TLS / mTLS。
3. 复用 GoCell 既有治理（`kernel/idempotency.Claimer` / `kernel/healthz` / `pkg/redaction` / `runtime/outbox` ConsumerBase），
   不另起平行通路。
4. examples/iotdevice 增加一个 MQTT 演示路径，证明 end-to-end 跑通；保留原 HTTP/WebSocket 主路径不变。

## 3. 非目标

- gRPC / Kafka adapter（V11-2 / V12-1 分别独立）
- MQTT 协议 v3.1.1 兼容（仅 v5；v3 留作未来 opt-in，本 spec 不实现）
- broker 侧管理控制台 / topic ACL 管理界面（broker 自己的事）
- 应用层 payload 加密（端到端 e2ee）—— TLS 已覆盖 in-transit；payload 层加密留 backlog
- MDM / 零信任 cells 实施（V11 ZT 系列另起）
- iotdevice 现有 HTTP/WebSocket 路径切换或下线

## 4. 用户场景

### Scenario A — Cell 产生事件，MQTT publish

```
cells/iotdevice/devicecell/slices/deviceregister
  └─ commit local tx
  └─ outbox.Emit("event.device.registered.v1", payload)
       ↓ relay
       ↓ adapters/mqtt.Publisher.Publish(ctx, topic, payload[QoS 1])
       ↓ broker (Mosquitto/EMQX)
       ↓ subscribers (MDM 控制台 / 监控 cell / 第三方系统)
```

验收：publish 失败时由 outbox relay backoff 重试；broker PUBACK 超时 → Requeue。

### Scenario B — Cell 订阅外部命令，handler 决定 Disposition

```
external MDM 控制台 publish "command.device.reboot.v1"
  ↓ broker
  ↓ adapters/mqtt.Subscriber → ConsumerBase wrap
  ↓ idempotency Claimer (entry.ID from MQTT v5 UserProperty `eventId`)
  ↓ slice handler returns Ack / Requeue / Reject
       ├─ Ack → PUBACK
       ├─ Requeue → 不 PUBACK + ConsumerBase 退避重试
       └─ Reject → publish 到 $dead/<originalTopic>; PUBACK 原消息
```

验收：handler 三态 Disposition 全部正确路由；幂等命中时不重复触发副作用。

### Scenario C — Reconnect 自愈

```
正常运行 → broker 重启 / 网络分区
  ↓ autopaho 内置 reconnect
  ↓ adapter 重建 subscription
  ↓ broker 补投未 PUBACK 的 QoS 1 消息（SessionExpiryInterval=600s）
  ↓ ConsumerBase 幂等去重
```

验收：分区恢复后无消息丢失、无重复副作用、readyz 503 → 200 自愈。

## 5. 验收标准 (Acceptance Criteria)

| ID | 标准 | 检查方式 |
|----|------|---------|
| AC-1 | `adapters/mqtt` 实现 `outbox.Publisher` 与 `outbox.Subscriber` 接口 | `go build ./...` + 接口实现断言测试 |
| AC-2 | outbox conformance suite (`kernel/outbox/outboxtest.TestPubSub`) 全部 Batch 通过 | `go test ./adapters/mqtt/...` |
| AC-3 | QoS 1 + PUBACK 重投场景，ConsumerBase 幂等去重正确 | integration test |
| AC-4 | TLS 1.2+ 强制、mTLS 经 `tls.Config` 注入 Vault PKI cert 路径可工作 | integration,mqtt_tls (nightly) |
| AC-5 | clientId 必须 `{cellID}-{role}-{uuid}` 格式；构造期 `Validate()` 校验 | unit + archtest `MQTT-CLIENT-ID-NAMESPACE-01` |
| AC-6 | adapter 内 topic 前缀 funnel（cell 只能 publish/subscribe 自己 namespace 的 topic）；PR-1 满足 sealed-struct 上游约束；下游 callsite funnel 在 PR-2/3 via gh #1225 | unit + archtest `MQTT-TOPIC-NAMESPACE-01` |
| AC-7 | readyz 注册 `mqtt_ready` typed probe，列入 `PROBENAME-SEALED-FUNNEL-01` inventory | unit + archtest |
| AC-8 | adapter 注册 `mqtt_*` metric（带 `cell` label）覆盖 publish / consume / dlx / reconnect / ack 延迟 / in-flight。spec 初列 6 名随实现细化为 **11 个**（全 `mqtt_` 前缀）：`mqtt_publish_total` / `mqtt_publish_failed_total` / `mqtt_publish_ack_duration_seconds` / `mqtt_consume_total` / `mqtt_consume_failed_total` / `mqtt_consume_duration_seconds` / `mqtt_consume_inflight` / `mqtt_reconnect_total` / `mqtt_subscribe_failed_total` / `mqtt_dlx_total` / `mqtt_dlx_failed_total`。两处对账（#1429）：`packet_too_large_total` 实现为 `mqtt_publish_failed_total{reason=payload_too_large}` reason-label；`subscribe_inflight` 实现为 `mqtt_consume_inflight` gauge（in-flight delivery 计数，复用 StopIntake drain atomic）。`ack_duration → mqtt_publish_ack_duration_seconds`、`packet_too_large_total`→reason-label、`subscribe_inflight`→`mqtt_consume_inflight` 的完整对账表 + 决策 + 5 个超集 metric 理由见 ADR-048 §Amendment 2026-06-07 — #1429；metric 名集 + reason 闭集由 archtest `MQTT-METRIC-LABEL-VALUES-FROZEN-01` 机器冻结。PR-1 仅注册 `reconnect_total`，其余随各 emitter 在 PR-2/3/4 落地 | unit + archtest `MQTT-METRIC-LABEL-VALUES-FROZEN-01` |
| AC-9 | payload 与 last_error 经 `pkg/redaction` 单源 redact；span attribute 经 `safeStringAttr` | unit + 既有 `SPAN-SETATTR-REDACT-01` 覆盖 |
| AC-10 | Reject → publish 到 `$dead/<topic>` 可观测、可由独立 subscriber 验证 | integration |
| AC-11 | examples/iotdevice 增加一条 MQTT 演示路径（事件发布或命令订阅，详 plan），smoke 通过 | example smoke |
| AC-12 | CI 集成测试经 testcontainers（`eclipse-mosquitto:2.0`）由 `CI-INTEGRATION-DISCOVERY-01` 自动并入 integration-test adapters shard（非独立 job / 非静态 service container）；该 job wall-time 由 integration-tier slowgate 阈值（120s，PR #1594 CI 实跑 PASS 验证）机器门控，超阈测试走 `SLOWGATE-ALLOWLIST-01` must-justify。原「pr-check 增量 ≤60s」因 broker-restart 重连测试本身 ~60s 不可孤立计量，#1430 重构为该门控；预算记录见 plan.md PR-2 §验收 | `.github/workflows/_build-lint.yml`（slowgate step）+ `tools/slowgate/allowlist.txt` |

## 6. 关键决策（来自 clarify 阶段）

| 决策 | 值 | 出处 |
|------|---|------|
| 协议版本 | MQTT v5 | clarify Q1 |
| 客户端库 | `github.com/eclipse/paho.golang/autopaho` v0.23.0 | clarify Q1 (updated from v0.12) |
| 范围 | Adapter + iotdevice 演示 | clarify Q2 |
| DLT 形态 | adapter app-level publish 到 `$dead/<topic>` | clarify Q3 |
| PR 切片 | 5 个 PR（详 plan.md），每片 ≤2000 行 net diff | clarify Q4 + 估算 |
| broker (CI) | `eclipse-mosquitto:2.0` 官方镜像 | explorer B |
| broker (unit) | `github.com/mochi-mqtt/server/v2` in-process | explorer B |
| 客户端身份 | 默认 username/password；mTLS via Vault PKI 可选 | explorer C |
| 错误分类 | 表见 plan.md §5 | explorer C |

## 7. 约束与约定

### 7.1 必须复用，不另建平行通路

- 幂等：`kernel/idempotency.Claimer`（**不**自实现 dedupe）
- 重试退避：`outbox.ExponentialDelay` + jitter（**不**引入第三方 backoff 库）
- ConsumerBase：`kernel/outbox.ConsumerBase` Wrap subscriber（**不**绕过）
- redaction：`pkg/redaction` (`RedactPayload` / `RedactString` / `SanitizeError`)
- healthz typed probe：`kernel/healthz.ReadyProbeName` const 声明
- 关闭：`runtime/adapterutil.CloseWithDeadline`

### 7.2 不向后兼容

- 仅支持 v5；不引 v3 双协议抽象（避免 ~25% 额外代码 + 测试矩阵翻倍）
- 不为 paho v3 留任何抽象层 / interface seam

### 7.3 archtest 新增（≥ Medium 评级）

| Archtest ID | 评级 | 作用 |
|-------------|------|------|
| `MQTT-CLIENT-ID-NAMESPACE-01` | Hard 上游（包外 compile gate）+ Medium 上游（包内 archtest A2）| sealed-struct field freeze (A1) + construction allowlist (A2) + no alias (A3)；下游 callsite funnel 延迟 PR-2/3 (#1225) |
| `MQTT-TOPIC-NAMESPACE-01` | Hard 上游（包外 compile gate）+ Medium 上游（包内 archtest A2）| 同上；下游 callsite funnel 延迟 PR-2/3 (#1225) |
| `PROBENAME-SEALED-FUNNEL-01` | 既有 Hard downstream，纳入 inventory | `mqtt_ready` const 入册（probeNameSanctionedPkgs + adapterSanctionedPkgs + goldenProbeNames）|
| `MESSAGE-CONST-LITERAL-01` | 既有 Hard | 新 errcode 调用站点零违例 |

无新 Soft 约束。

## 8. 风险

| 风险 | 严重性 | 缓解 |
|------|-------|------|
| paho v5 autopaho API 仍在演化（v0.12.x） | 中 | 在 `adapters/mqtt/connection.go` 内 wrap，单点替换；测试 conformance 套独立验证 |
| Mosquitto 2.0 默认 anonymous 关闭，CI conf 文件要 mount | 低 | testmain_integration 写 conf 临时文件，文档化 |
| iotdevice demo 选哪个 slice 可能与 HTTP/WS 路径耦合 | 中 | 仅在 demo PR 探查；保留原路径不动；如成本超 1000 行则改用独立 `examples/mqttdemo`（plan §7 fallback） |
| MQTT broker 行为差异（Mosquitto vs EMQX）影响 conformance | 中 | conformance 套以 Mosquitto 为 CI 基线；EMQX 兼容性留 backlog 不阻塞本 PR 系列 |
| nightly TLS/ACL 测试在 CI 上不稳定 | 中 | 显式 `//go:build integration,mqtt_tls`，PR 关键路径不依赖 |

## 9. 不在本 spec 决议（留到 plan/实施阶段）

- iotdevice demo 具体走 publish (Scenario A 风格) 还是 subscribe (Scenario B 风格)？
  → plan §7 决策（preferred: publish on `device.registered.v1`，与现有 outbox 通道并行落 MQTT，
  保留原 HTTP 路径，subscribe 端用独立 verify cell 或 smoke 脚本）
- 是否在 `cells/internal/`（如已存在）新建 mqtt-bound consumer？或仅在 example 内验证？
  → plan §7 决策

## 10. 关联

- ADR (待写): `docs/architecture/{TS}-adr-mqtt-adapter.md` —— 含 v5 / autopaho / DLT app-level / clean=true + clientId 唯一 决策记录
- Roadmap: `docs/product/roadmap/202605042330-001-v1.1能力路线-零信任mdm基础.md` §2 V11-4
- 参考实现: `adapters/rabbitmq/`（接口范本）
- 测试范本: `kernel/outbox/outboxtest` (conformance), `adapters/rabbitmq/integration_test.go` (testcontainers 模式)
