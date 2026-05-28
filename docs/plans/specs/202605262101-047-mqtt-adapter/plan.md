# Implementation Plan — MQTT Adapter (V11-4)

> Spec: [`spec.md`](./spec.md). 本文件落"怎么做"。

| 字段 | 值 |
|------|---|
| 总规模估算 | ~5400-6000 行 net diff（prod ~1900 / test ~2900 / archtest+ADR+CI+docs ~600-800 / iotdevice demo ~700-900） |
| PR 数量 | 5 |
| 每 PR 行数上限 | ≤ 2000 行（用户硬约束） |
| 实施次序 | 严格 PR-1 → PR-2 → PR-3 → PR-4 → PR-5（PR 间依赖见 §6） |

---

## 1. 对标参考（ref:）

| 模块 | 参考 | 取用 |
|------|------|------|
| autopaho client lifecycle | `eclipse/paho.golang/autopaho` v0.23.0 godoc | reconnect / SessionExpiry / WaitConnected |
| Connection wrap 模式 | `adapters/rabbitmq/connection.go` | reconnect goroutine / health / closeCh |
| Backoff | `adapters/adapterutil/backoff.go` 共享 helper（从 rabbitmq 提取）+ jitter | 单源治理，rabbitmq 同步重构使用同一实现，**不**引第三方库 |
| KeyNamespace funnel | `adapters/redis/keyns.go` + archtest `REDIS-KEY-NAMESPACE-01` | clientID + topic 两个 typed namespace |
| Readyz typed probe | `adapters/postgres.ProbeReady` + `kernel/healthz.ReadyProbeName` | `mqtt_ready` const |
| Metrics cell label | `adapters/otel/metric_provider.go` + `kernel/observability/metrics` | 6 个 metric 注册 |
| Redaction 接入 | `pkg/redaction` + `runtime/outbox.SanitizeError` | publish payload / last_error |
| Outbox conformance | `kernel/outbox/outboxtest/conformance.go` (TestPubSub) | adapter 实例传入 6 Batch |
| Integration testcontainers | `adapters/rabbitmq/testmain_integration_test.go` | Mosquitto GenericContainer 启动 |

每个 commit message 必须含 `ref:` 注明对标点。

---

## 2. 文件清单（按依赖序）

### 2.1 生产代码 (adapters/mqtt/)

| 文件 | 估算行数 | 内容 |
|------|---------|------|
| `doc.go` | 80 | package godoc + INVARIANT 锚点（archtest 入口） |
| `config.go` | 250 | `Config{ClientID, Brokers, TLS, SessionExpiry, Auth, Backoff, ...}` + `Validate()` |
| `clientid.go` | 100 | `type ClientID struct{value string}` (sealed struct) + `ParseEphemeralClientID(cellID, role string) (ClientID, error)` + `ParseStableClientID(cellID, role, instanceID string) (ClientID, error)` |
| `topicns.go` | 130 | `type TopicNamespace struct{value string}` (sealed struct) + `ParseTopicNamespace(ns string) (TopicNamespace, error)` + `PublishOK` / `SubscribeOK` funnel |
| `connection.go` | 550 | autopaho wrap + reconnect 循环 + `Health() error` + `Close(ctx)` + permanentErr 状态机 |
| `publisher.go` | 220 | `Publisher` 实现：`Publish(ctx, topic, payload) error`，QoS 1 + WaitConnected + 超时 |
| `subscriber.go` | 380 | `Subscriber` 三段式（Setup/Ready/Subscribe），handler 三态 Disposition 路由 |
| `deadletter.go` | 100 | `routeReject(originalTopic, entry) error`，publish 到 `$dead/<topic>` |
| `metrics.go` | 220 | PR-1 仅注册 `reconnect_total` metric（其余 5 个随 emitter 在 PR-2/3/4 落地；见 AC-8 note） |
| `healthz.go` | 80 | `const ProbeReady healthz.ProbeName = "mqtt_ready"` + `Probes()` on Connection |
| `redact.go` | 80 | `redactPayloadForLog` / `redactConnectURL`（小 wrapper，单源 `pkg/redaction`） |
| `errors.go` | 120 | `ErrAdapterMQTTPayloadTooLarge` 等 sentinel + `classifyConnackReason` |

**生产小计**: ~2310 行（含注释，实际净 diff 行 ~1900 行）

### 2.2 测试 (adapters/mqtt/)

| 文件 | 估算行数 | 内容 |
|------|---------|------|
| `config_test.go` | 150 | Validate 边界 |
| `clientid_test.go` | 100 | Validate / Parse |
| `topicns_test.go` | 140 | namespace 拒绝 / 允许矩阵 |
| `connection_test.go` | 400 | reconnect / closeCtx / permanentErr / health |
| `publisher_test.go` | 320 | QoS1 / payload too large / publish timeout / order |
| `subscriber_test.go` | 500 | Ack/Requeue/Reject / ConsumerBase 接入 / claim 失败降级 |
| `deadletter_test.go` | 180 | DLT 路由 / publish 失败 fallback |
| `metrics_test.go` | 230 | PR-1: reconnect_total only（其余 5 个 metric 随 emitter 在 PR-2/3/4 落地；见 AC-8）|
| `healthz_test.go` | 80 | probe 注册 + nil checker 拒绝 |
| `redact_test.go` | 100 | payload key list / connect URL credentials strip |
| `errors_test.go` | 120 | connack reason → disposition 矩阵 |
| `conformance_test.go` | 200 | 跑 `outboxtest.TestPubSub`，Features 声明 |
| `integration_test.go` | 500 | build tag `integration`：mochi/mosquitto + 完整端到端 |
| `integration_tls_test.go` | 200 | build tag `integration,mqtt_tls`：mTLS + self-signed CA |
| `testmain_integration_test.go` | 120 | testcontainers Mosquitto 启动 |

**测试小计**: ~3340 行（净 ~2900 行）

### 2.3 Archtest

| 文件 | 估算行数 | 内容 |
|------|---------|------|
| `tools/archtest/mqtt_funnel_test.go` | 280 | `MQTT-CLIENT-ID-NAMESPACE-01` + `MQTT-TOPIC-NAMESPACE-01`（sealed-struct field freeze + construction allowlist + no alias）；下游 callsite funnel deferred to PR-2/3 (#1225) |

### 2.4 ADR / 文档

| 文件 | 估算行数 | 内容 |
|------|---------|------|
| `docs/architecture/{TS}-adr-mqtt-adapter.md` | 220 | v5 / autopaho / DLT app-level / clientId 唯一 决策与威胁矩阵 |
| `docs/references/framework-comparison.md` | +15 | 添加 paho.golang 行 |

### 2.5 CI / 工具

| 文件 | 估算行数 | 内容 |
|------|---------|------|
| `.github/workflows/_build-lint.yml` | +30 | integration-test job 通过 testcontainers 自动发现新增的 `adapters/mqtt` integration 包（CI-INTEGRATION-DISCOVERY-01 自动接入，无需静态 service container 配置） |
| `go.mod` / `go.sum` | +30 | autopaho v0.12 + mochi v2.7 + paho.golang/paho v0.21 |

### 2.6 iotdevice demo

| 文件 | 估算行数 | 内容 |
|------|---------|------|
| `examples/iotdevice/contracts/event/device-registered/...` | +60 | 增加 `transports: [amqp, mqtt]` 标记（如果 codegen 支持），否则在 cell 内多通道发布 |
| `examples/iotdevice/cells/devicecell/slices/deviceregister/...` | +200 | publish 路径多走一份 MQTT（与原 outbox 通道并行；可由 config flag 切换） |
| `examples/iotdevice/configs/dev.yaml` | +30 | mqtt broker 配置 |
| `examples/iotdevice/test/mqtt_smoke_test.go` | +400 | smoke：iotdevice register → MQTT broker → 独立 subscriber 收到 → 校验 payload |
| `examples/iotdevice/docs/mqtt.md` | +120 | 演示说明、跑通命令 |

**iotdevice 小计**: ~810 行

---

## 3. PR 切片表（每片 ≤ 2000 行 net diff）

> 行数估算 = 列表行数 + 20% margin（含 commit/PR 描述、godoc、注释）。

### PR-1 / 047-pr1: 基础设施 + Typed Funnel + ADR

**估算 ~1700 行净 diff (生产 ~860 / 测试 ~640 / archtest+ADR+gomod ~200)**

| 引入 | 文件 |
|------|------|
| package skeleton | `adapters/mqtt/doc.go`, `errors.go`, `redact.go` |
| Config + funnel | `config.go`, `clientid.go`, `topicns.go` |
| Health/metrics | `healthz.go`, `metrics.go` |
| Connection | `connection.go`（含 autopaho wrap + reconnect 循环 + Close(ctx)） |
| 对应单元测试 | `config_test.go`, `clientid_test.go`, `topicns_test.go`, `healthz_test.go`, `metrics_test.go`, `redact_test.go`, `errors_test.go`, `connection_test.go` |
| Archtest | `tools/archtest/mqtt_funnel_test.go`（两个 funnel + inventory 入册） |
| Docs | ADR `docs/architecture/{TS}-adr-mqtt-adapter.md` + `framework-comparison.md` +15 行 |
| 依赖 | `go.mod` / `go.sum`：autopaho + paho v5 + mochi-mqtt（mochi 仅 test 依赖） |

**验收**：
- `go build ./...` 通过；`go test ./adapters/mqtt/... -short`（仅 unit）通过；`go test ./tools/archtest/...` 通过；`golangci-lint` 0 issue。
- Connection 单元测试用 mochi-mqtt 内嵌 broker；不引入 testcontainers / mosquitto。
- 单 reviewer（diff <2000，按 ship §7 阶段 7）。

### PR-2 / 047-pr2: Publisher + CI Integration 基础

**估算 ~1500 行净 diff (生产 ~220 / 测试 ~820 / CI+conf ~50 / testcontainers wrap ~200 / int-fixture +200)**

| 引入 | 文件 |
|------|------|
| Publisher 实现 | `adapters/mqtt/publisher.go` |
| Publisher 单测 | `publisher_test.go`（in-process mochi） |
| 集成测试基础 | `testmain_integration_test.go`（`testcontainers.GenericContainer` 起 `eclipse-mosquitto:2.0`，in-memory conf mount + wait.ForLog strategy；非 docker-compose static service container） |
| 集成测试 publisher | `integration_test.go` PR-2 部分（仅 publisher 场景：QoS1 / broker 断连重连 / publish timeout） |
| CI | `.github/workflows/_build-lint.yml` 通过 testcontainers auto-discovery（`CI-INTEGRATION-DISCOVERY-01`）接入，无需手动添加 mqtt service 配置 |

**验收**：unit + integration（testcontainers eclipse-mosquitto:2.0）publisher 路径全过；adapters shard CI 实测时长 +<60s（待 CI 实测回填；计划估算基于 rabbitmq integration 同类参考值）。

### PR-3 / 047-pr3: Subscriber + ConsumerBase 接入

**估算 ~1700 行净 diff (生产 ~380 / 测试 ~880 / 集成 ~200 / errors 调整 ~50 / docs ~50)**

| 引入 | 文件 |
|------|------|
| Subscriber 实现 | `adapters/mqtt/subscriber.go`（Setup/Ready/Subscribe 三段式，wrap ConsumerBase） |
| Subscriber 单测 | `subscriber_test.go`（in-process mochi + fake Claimer） |
| 集成测试 subscriber | `integration_test.go` PR-3 部分（Disposition 三态 + clean=false 会话恢复 + clientId 冲突） |
| Errors 扩展 | SUBACK reason 分类 + bootstrap fail-fast |

**验收**：handler 三态 Disposition 矩阵全过；ConsumerBase Claim 失败降级到 Requeue 验证；clean=false 离线消息补投 integration 验证。

### PR-4 / 047-pr4: DLT 路由 + Outbox Conformance + TLS Nightly

**估算 ~1300 行净 diff (生产 ~100 / 测试 ~750 / nightly ~200 / 调试与 polish ~200)**

| 引入 | 文件 |
|------|------|
| DLT 路由 | `adapters/mqtt/deadletter.go` + `deadletter_test.go` |
| Conformance | `adapters/mqtt/conformance_test.go`（adapter 装入 `outboxtest.TestPubSub`） |
| TLS / mTLS | `integration_tls_test.go`（build tag `integration,mqtt_tls`） |
| CI nightly | `.github/workflows/archtest-nightly.yml` 加 `mqtt_tls` build tag matrix entry |
| Polish | Subscriber Reject 走 DLT 集成完整、ConsumerBase Reject path end-to-end |

**验收**：outbox conformance 6 Batch 全过；DLT 路由可由独立 subscriber 验证；TLS/mTLS nightly 通过。

### PR-5 / 047-pr5: examples/iotdevice MQTT 演示路径

**估算 ~900 行净 diff (示例代码 ~250 / smoke 测试 ~400 / docs ~130 / 配置 ~30 / contract +60 / 调整 ~30)**

| 引入 | 文件 |
|------|------|
| iotdevice MQTT 通道 | `examples/iotdevice/cells/devicecell/slices/deviceregister`（增量加 mqtt publish 并行通道，由 config flag 切换；不替换原 HTTP/WS） |
| Contract 标记 | `examples/iotdevice/contracts/event/device-registered`（transports 加 mqtt；若 codegen 不支持则 cell 内 publish 二次落） |
| 配置 | `examples/iotdevice/configs/dev.yaml` 加 mqtt section |
| Smoke 测试 | `examples/iotdevice/test/mqtt_smoke_test.go`（in-process broker，端到端 register→broker→subscriber→verify payload） |
| 文档 | `examples/iotdevice/docs/mqtt.md` |

**验收**：iotdevice register 流程同时落 RabbitMQ 与 MQTT 两个通道；MQTT subscriber 验证 payload 一致；原 HTTP/WS 主路径回归通过；smoke 在 PR CI 上跑通。

**Fallback 触发**（spec §8 风险 #3）：若 iotdevice 改动估算 >1500 行（即 PR-5 >2000 行），改用新建 `examples/mqttdemo/` 独立 example（不动 iotdevice），范围更小，~600-800 行。该决策在 PR-5 写代码前由 developer agent 用 `git diff --numstat` 实测后切换。

---

## 4. 并行批次分析（实施阶段）

PR-1 ~ PR-5 必须**串行** ship（每 PR 依赖前者已 merge）。**单 PR 内**任务并行如下：

### PR-1 内（5 个 developer agent 上限 4，按文件无交叉拆）

| 批次 | 文件 | 并行性 |
|------|------|-------|
| B1 | `doc.go`, `errors.go`, `redact.go`, `config.go`, `clientid.go`, `topicns.go` + 对应 test | agent-1（独立小文件，单 agent 串行） |
| B2 | `healthz.go`, `metrics.go` + test | agent-2 |
| B3 | `connection.go` + `connection_test.go` | agent-3（大文件，单 agent 专注） |
| B4 | `tools/archtest/mqtt_funnel_test.go` + ADR | agent-4 |
| go.mod / go.sum | 主 agent 整合 | — |

B1-B4 完全无文件交叉，可 4 路并行。

### PR-2 ~ PR-5 单 PR 内任务量较小，1-2 agent 串行即可。

---

## 5. 错误分类映射 → Disposition（来自 explorer C，落入 errors.go）

| 错误场景 | errcode.Kind | Disposition | 备注 |
|----------|-------------|-------------|------|
| 网络超时 / TCP Reset | `KindUnavailable` (WrapInfra) | Requeue | autopaho 自愈 |
| CONNACK 0x81/0x82 (protocol/identifier) | `KindInternal` | bootstrap fail-fast（不进 Disposition 循环） | 配置错 |
| CONNACK 0x87 (Not Authorized) | `KindInternal` permanentErr | Requeue（重连继续）+ readyz 503 | operator fix |
| SUBACK 0x80 (Not Authorized) | `KindInternal` | bootstrap fail-fast | ACL 配置错 |
| SUBACK 0x97 (Rate limited) | `KindUnavailable` | Requeue + 退避 | 瞬态 |
| Payload > MaximumPacketSize | `KindValidation` | Reject | 永久 |
| Malformed payload (unmarshal fail) | `KindValidation` | Reject | 永久 |
| TLS handshake fail (x509) | `KindInternal` | Reject + bootstrap fail-fast | cert 错 |
| QoS1 PUBACK timeout | `KindUnavailable` | Requeue | 瞬态 |

---

## 6. PR 间依赖与 ship 节奏

```
PR-1 (基础设施) ──╮
                  ├─► PR-2 (Publisher) ──► PR-3 (Subscriber) ──► PR-4 (DLT+conformance) ──► PR-5 (iotdevice demo)
                  ╰─ ADR & funnel 提前固定
```

每 PR ship 节奏（按 ship skill 阶段 7 等级分档）：
- PR-1 ~1700 行 → 3 reviewer (600 ≤ diff < 1500 段位的上限附近；按区间归档 1500-2000 行落 6 reviewer 段位 → **6 reviewer**)
- PR-2 ~1500 行 → 6 reviewer
- PR-3 ~1700 行 → 6 reviewer
- PR-4 ~1300 行 → 3 reviewer
- PR-5 ~900 行 → 2 reviewer

> ship §7：`diff ≥ 1500` 走 6 reviewer；`600 ≤ diff < 1500` 走 3 reviewer。

---

## 7. iotdevice demo 决策（spec §9 留白）

**首选**（在 PR-5 进入 worktree 后 1h 内决策）：
- 在 `deviceregister` slice 的 commit-tx-after hook 内，**并行**走 MQTT publish（用本 adapter）。
- 原 RabbitMQ outbox 通道保持不变；新 MQTT 通道只是镜像，验证 wire-side 正常即可。
- contract.yaml 若不支持 `transports: [amqp, mqtt]` 双标，则在 cell 内显式调两次 Publish（一份 amqp/一份 mqtt），不动 codegen。

**Fallback**：iotdevice 内改动估算 > 1500 行净 diff → 切到 `examples/mqttdemo/` 新 example。

不在本 plan 决议 codegen 是否扩 `transports` 多值——那是 V11 后续单独工作，本 PR 系列不引。

---

## 8. TDD 测试先写清单（每 PR 内部）

每 PR 进入 worktree 后，按 ship skill 阶段 4 先写 `*_test.go` 跑 FAIL，再写实现。先写顺序：

### PR-1
1. `clientid_test.go` 全部 case → 跑 FAIL（无 Parse）
2. `topicns_test.go` 全部 case → FAIL
3. `config_test.go` Validate boundary → FAIL
4. `connection_test.go` Connect+Health+Close+reconnect → FAIL
5. `healthz_test.go` / `metrics_test.go` / `redact_test.go` / `errors_test.go` → FAIL
6. archtest funnel 测试 → FAIL（funnel 文件还没建）
7. 然后写实现 → 全部 PASS。

### PR-2 ~ PR-5
同模式：先 test 后实现。conformance / integration 的 broker 启动是测试基础设施，先于 case 写。

---

## 9. 反思自检（per "方案与计划原则"）

| 项 | 自查 | 结论 |
|----|------|------|
| 彻底 | 是否还有 TODO / 兼容 / 后续 PR 标记？ | 5 PR 之外无 TODO；ZT mTLS（V11 ZT-1/2）属 roadmap 独立 phase，不算本 spec 拖尾。MQTT v3 兼容明确 OUT。 |
| 不向后兼容 | 是否引入 deprecation 别名 / v3 兼容 shim / 双协议抽象层？ | 否。仅 v5。 |
| 优雅简洁 | 能否更少代码达成？ | autopaho 内置 reconnect 复用 → 比 rabbitmq 自实现少 ~400 行；ExponentialDelay 复用避免新 backoff 包；conformance 套复用避免新测试框架；DLT 选 app-level 避免 broker 特定插件依赖。无新抽象层引入。 |

无 deferred carve-out。无新 Soft archtest 立项。

---

## 10. 关联文件落地路径

- spec: `docs/plans/specs/202605262101-047-mqtt-adapter/spec.md`
- plan: `docs/plans/specs/202605262101-047-mqtt-adapter/plan.md` (本文)
- tasks: `docs/plans/specs/202605262101-047-mqtt-adapter/tasks.md` (下一步生成)
- ADR (PR-1 内 commit): `docs/architecture/<TS>-adr-mqtt-adapter.md`
