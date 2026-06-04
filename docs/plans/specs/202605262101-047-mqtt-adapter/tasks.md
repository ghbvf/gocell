# Tasks — MQTT Adapter (V11-4)

> Spec: [`spec.md`](./spec.md) · Plan: [`plan.md`](./plan.md)
>
> 每条任务格式：`T-<PR>.<seq>` · 文件 · 估算 LOC · 前置依赖 · TDD 顺序（`test-first` / `impl` / `both`）。
> "PR 切片"对应 plan.md §3。每 PR 内部 T 任务允许并行；PR 间严格串行。

---

## PR-1 / 047-pr1: 基础设施 + Typed Funnel + ADR

| ID | 子任务 | 文件 | LOC | 前置 | TDD |
|----|--------|------|-----|------|-----|
| T-1.0 | go.mod 添加 paho.golang/autopaho v0.23.0, paho.golang/paho v0.21+, mochi-mqtt/server/v2 v2.7+（注：v0.23.0 较 spec 草稿 v0.12 更新，含 WaitConnected/AwaitConnection 等接口） | `go.mod` `go.sum` | 30 | — | impl |
| T-1.1 | 写 `errors.go` — 定义 `ErrAdapterMQTTPayloadTooLarge` / `ErrAdapterMQTTSubscribePermanent` 等 sentinel (errcode.New) | `adapters/mqtt/errors.go` `errors_test.go` | 240 | T-1.0 | both |
| T-1.2 | 写 `redact.go` — `redactConnectURL(string) string` / payload log helper (单源 `pkg/redaction`) | `adapters/mqtt/redact.go` `redact_test.go` | 180 | T-1.0 | both |
| T-1.3 | 写 `clientid.go` — `type ClientID struct{value string}` (sealed struct) + `ParseClientID(cellID, role string) (ClientID, error)` | `adapters/mqtt/clientid.go` `clientid_test.go` | 200 | T-1.0 | test-first |
| T-1.4 | 写 `topicns.go` — `type TopicNamespace struct{value string}` (sealed struct) + `ParseTopicNamespace(ns string) (TopicNamespace, error)` + `PublishOK(topic) error` / `SubscribeOK(filter) error` | `adapters/mqtt/topicns.go` `topicns_test.go` | 270 | T-1.0 | test-first |
| T-1.5 | 写 `config.go` — `Config{ClientID, Brokers, TLS, SessionExpiry, Auth, Backoff, MaximumPacketSize, ConnectTimeout, ...}` + `Validate()` | `adapters/mqtt/config.go` `config_test.go` | 400 | T-1.3, T-1.4 | test-first |
| T-1.6 | 写 `healthz.go` — `const ProbeReady healthz.ProbeName = "mqtt_ready"` + `Probes()` method on Connection | `adapters/mqtt/healthz.go` `healthz_test.go` | 160 | T-1.0 | both |
| T-1.7 | 写 `metrics.go` — PR-1 注册 `reconnect_total` metric（含 cell label）；其余 5 metric（publish_total / ack_duration / subscribe_inflight / dlx_total / packet_too_large_total）随各自 emitter 在 PR-2/3/4 落地（AC-8 split）| `adapters/mqtt/metrics.go` `metrics_test.go` | 450 | T-1.0 | both |
| T-1.8 | 写 `connection.go` — autopaho client wrap：`Open(ctx, Config) (*Connection, error)` / `Client()` / `Health() error` / `Close(ctx)`；reconnect 由 autopaho 内置，adapter 仅注入 backoff + permanentErr 状态机 | `adapters/mqtt/connection.go` `connection_test.go` | 950 | T-1.5, T-1.6, T-1.7 | test-first |
| T-1.9 | 写 `doc.go` — package godoc + 文件头 `// INVARIANT:` 锚点（archtest 入口） | `adapters/mqtt/doc.go` | 80 | T-1.1..1.8 | impl |
| T-1.10 | 写 archtest `MQTT-CLIENT-ID-NAMESPACE-01` + `MQTT-TOPIC-NAMESPACE-01`（sealed-struct field freeze A1 + construction allowlist A2 + no alias A3）；下游 callsite funnel deferred to PR-2/3, tracked #1225 | `tools/archtest/mqtt_funnel_test.go` | 280 | T-1.3, T-1.4 | test-first |
| T-1.11 | `mqtt_ready` 加入 `PROBENAME-SEALED-FUNNEL-01` inventory（probeNameSanctionedPkgs + adapterSanctionedPkgs + goldenProbeNames）| `tools/archtest/probename_sealed_funnel_test.go` (改) | +10 | T-1.6 | impl |
| T-1.12 | 写 ADR `<TS>-adr-mqtt-adapter.md` — v5 选型 / autopaho / DLT app-level / clientId 唯一 / 威胁矩阵 | `docs/architecture/<TS>-adr-mqtt-adapter.md` | 240 | T-1.0..1.10 | impl |
| T-1.13 | 更新 `docs/references/framework-comparison.md` 加 paho.golang 行 | `docs/references/framework-comparison.md` | 15 | T-1.12 | impl |

**PR-1 净 diff 估算**: ~1700 行

**并行批次**：
- B1 (agent-1): T-1.1, T-1.2, T-1.3, T-1.4
- B2 (agent-2): T-1.5, T-1.6, T-1.7
- B3 (agent-3): T-1.8（依赖 B2）
- B4 (agent-4): T-1.10, T-1.11, T-1.12, T-1.13（部分依赖 B1/B3）
- B1, B2 并行；B3 等 B2 完；B4 收尾期可与 B3 并行（archtest 在 funnel 文件落地后）。

---

## PR-2 / 047-pr2: Publisher + CI Integration 基础

| ID | 子任务 | 文件 | LOC | 前置 | TDD |
|----|--------|------|-----|------|-----|
| T-2.1 | 写 `publisher.go` — `Publisher{conn *Connection}` + `Publish(ctx, topic, payload) error`（QoS 1, WaitConnected, packet size 校验, timeout, metric 计数） | `adapters/mqtt/publisher.go` `publisher_test.go` | 540 | PR-1 merged | test-first |
| T-2.2 | testcontainers wrap — `StartMosquittoContainer(t)` + wait `1883/tcp` + mount minimal conf (anonymous 允许 + listener) | `adapters/mqtt/testmain_integration_test.go` | 200 | T-2.1 | impl |
| T-2.3 | `integration_test.go` Publisher 场景（build tag `integration`）：QoS1 ack 正常 / broker 慢响应 timeout / broker 断连重连后 publish 恢复 / publish 到无 subscriber topic | `adapters/mqtt/integration_test.go` (新增 PR-2 部分) | 500 | T-2.2 | test-first |
| T-2.4 | CI workflow：`.github/workflows/_build-lint.yml` integration-test job 加 mosquitto service container（image: eclipse-mosquitto:2.0, port 1883/8883, mount mosquitto.conf） | `.github/workflows/_build-lint.yml` | 40 | T-2.3 | impl |
| T-2.5 | mosquitto.conf fixture（anonymous true + listener 1883 + persistence false） | `adapters/mqtt/testdata/mosquitto.conf` | 25 | T-2.2 | impl |

**PR-2 净 diff 估算**: ~1500 行

**并行批次**：T-2.1 与 T-2.2/T-2.5 可并行（不同文件）；T-2.3 等前两者完；T-2.4 收尾。

> **T-2.4 实施偏离 + AC-12 对账（#1430）**：T-2.4/T-2.5 原计划在 integration-test job 加静态 mosquitto **service container** + `mosquitto.conf` fixture。实际交付改用 **testcontainers in-test 自管 broker**（`testmain_integration_test.go`）+ `CI-INTEGRATION-DISCOVERY-01` 自动发现并入 adapters shard——无静态 service container、无独立 job。AC-12「pr-check 增量 ≤60s」因单个 broker-restart 重连测试本身 ~60s 而孤立计量不可达，#1430 将其重构为 **integration-tier slowgate 机器门控**（`_build-lint.yml` integration step 经 `slowgate --threshold=120s`，保守初值后续按真实 CI 收紧），并补 wall-time 预算记录于 plan.md PR-2 §验收。决策：保留并入、不回归静态 service container。

---

## PR-3 / 047-pr3: Subscriber + ConsumerBase 接入

| ID | 子任务 | 文件 | LOC | 前置 | TDD |
|----|--------|------|-----|------|-----|
| T-3.1 | 写 `subscriber.go` — `Subscriber` 三段式（Setup/Ready/Subscribe）；Subscribe 内 wrap ConsumerBase；message → outbox.Entry 映射（v5 UserProperty `eventId` → entry.ID）；Disposition 三态处理（Ack/Requeue/Reject 路由） | `adapters/mqtt/subscriber.go` | 480 | PR-2 merged | test-first |
| T-3.2 | `subscriber_test.go` — unit（in-process mochi + fake Claimer）：Ack PUBACK / Requeue 不 PUBACK / Reject 时调 DLT helper 桩 / ZeroValueDisposition → Requeue / ConsumerBase Claim 失败 → Requeue | `adapters/mqtt/subscriber_test.go` | 620 | T-3.1 | test-first |
| T-3.3 | `errors.go` 扩展 — `classifySubackReason(rc uint8) error` → 永久 (Reject + bootstrap fail-fast) vs 瞬态 (Requeue) | `adapters/mqtt/errors.go` (改) `errors_test.go` (扩) | 100 | T-3.1 | both |
| T-3.4 | `integration_test.go` Subscriber 场景：Disposition 三态实跑 / clean=false 会话恢复（SessionExpiryInterval）/ clientId 冲突（双连） / SUBACK rc=0x97 限流 → 重试 | `adapters/mqtt/integration_test.go` (扩 PR-3 部分) | 400 | T-3.1, T-3.3 | test-first |
| T-3.5 | （观察）确认 outbox conformance suite 输入接口与 PR-3 实现一致；如发现 gap 补 connection/publisher 微调 | — | <30 | T-3.1 | impl |

**PR-3 净 diff 估算**: ~1700 行

**并行批次**：T-3.1 + T-3.3 (errors 扩展) 可串行；T-3.2 紧跟 T-3.1；T-3.4 单独 agent。

---

## PR-4 / 047-pr4: DLT 路由 + Outbox Conformance + TLS Nightly

| ID | 子任务 | 文件 | LOC | 前置 | TDD |
|----|--------|------|-----|------|-----|
| T-4.1 | 写 `deadletter.go` — `routeDeadLetter(ctx, originalTopic string, payload []byte, reason ConsumeFailureReason)`：publish 到 `$dead/<topic>`，fail-closed（失败 log + `mqtt_dlx_failed_total` + ack-drop） | `adapters/mqtt/deadletter.go` `deadletter_test.go` | 280 | PR-3 merged | test-first |
| T-4.2 | `subscriber.go` 接入 `routeDeadLetter`（替换 PR-3 内的桩） | `adapters/mqtt/subscriber.go` (改) | <30 | T-4.1 | impl |
| T-4.3 | 写 `conformance_test.go` — 实例化 Publisher+Subscriber，Features 声明（GuaranteedOrder=false, **SupportsRequeue=false**（Option C, ADR-050 §6）, SupportsReject=true, **BroadcastSubscribe=false**（$share=competing consumers）），跑 `outboxtest.TestPubSub` 6 Batch | `adapters/mqtt/conformance_test.go` | 200 | T-4.2 | test-first |
| T-4.4 | 写 `integration_tls_test.go` — build tag `integration,mqtt_tls`；self-signed CA + client cert；mTLS handshake 正常 + handshake 失败（cert 未信任）| `adapters/mqtt/integration_tls_test.go` `testdata/tls/*` | 280 | T-4.2 | test-first |
| T-4.5 | CI nightly（实施偏离：见下）：新建独立 `.github/workflows/mqtt-tls-nightly.yml`（testcontainer 作业模型），**不**改 archtest-nightly | `.github/workflows/mqtt-tls-nightly.yml` | 40 | T-4.4 | impl |

> **T-4.5 实施偏离（PR-4 #1142，用户确认）**：原计划在 `archtest-nightly.yml` 加 `mqtt_tls` matrix entry，实际新建独立 `mqtt-tls-nightly.yml`。理由：archtest-nightly 是静态分析 shard 模型（跑 `./tools/archtest/...`，无 Docker），而 mTLS 测试需 testcontainer（mosquitto TLS listener），语义不匹配；独立 workflow 仿 `otel-collector-nightly.yml`（已有 testcontainer nightly 先例）更正确。`{integration, mqtt_tls}` 已注册进 `KnownNonDefaultTags`（fail-closed 自检 `TestKnownNonDefaultTagsCoverage`）。
| T-4.6 | 集成测试 DLT 端到端：subscribe + handler Reject → 独立 subscriber 在 `$dead/<topic>` 收到 | `adapters/mqtt/integration_test.go` (扩 PR-4 部分) | 180 | T-4.1, T-4.2 | test-first |

**PR-4 净 diff 估算**: ~1300 行

**并行批次**：T-4.1 / T-4.4 并行；T-4.2/T-4.3 串行；T-4.5 收尾。

---

## PR-5 / 047-pr5: examples/iotdevice MQTT 演示路径

| ID | 子任务 | 文件 | LOC | 前置 | TDD |
|----|--------|------|-----|------|-----|
| T-5.0 | （决策窗口）在 worktree 开始 1h 内：扫描 iotdevice `deviceregister` slice 改动估算；若 > 1500 行净 diff 改用新建 `examples/mqttdemo/` | — | — | PR-4 merged | impl |
| T-5.1 | 在 `examples/iotdevice/cells/devicecell/slices/deviceregister` 加并行 MQTT publish 通道（config flag 启用；原 outbox 通道保持） | `examples/iotdevice/cells/devicecell/slices/deviceregister/...` | 250 | T-5.0 | test-first |
| T-5.2 | contract.yaml 调整 — `examples/iotdevice/contracts/event/device-registered` 加 mqtt transport 标记（若 codegen 不支持双 transport 则不动 contract, 由 cell 内显式两次 publish） | `examples/iotdevice/contracts/event/device-registered/...` | 60 | T-5.1 | impl |
| T-5.3 | `examples/iotdevice/configs/dev.yaml` — 加 mqtt section（brokers / clientId 模板 / sessionExpiry） | `examples/iotdevice/configs/dev.yaml` | 30 | T-5.1 | impl |
| T-5.4 | smoke 测试 — `examples/iotdevice/test/mqtt_smoke_test.go`：起 mochi broker → iotdevice register API → 校验 amqp 通道 publish + mqtt 通道 publish 都收到；subscriber 端解析 payload 一致 | `examples/iotdevice/test/mqtt_smoke_test.go` | 450 | T-5.1, T-5.2, T-5.3 | test-first |
| T-5.5 | 文档 `examples/iotdevice/docs/mqtt.md` — 演示说明 + 跑通命令 + 限制（v5 only / clientId 模板 / 端到端测试入口） | `examples/iotdevice/docs/mqtt.md` | 130 | T-5.1..5.4 | impl |

**PR-5 净 diff 估算**: ~900 行（Fallback 路径估算 ~700 行更小）

**并行批次**：T-5.1 + T-5.2 + T-5.3 (文件无交叉) 并行；T-5.4 等前者完；T-5.5 收尾。

---

## 跨 PR 验证清单（每 PR ship 前）

| 项 | 命令 / 检查 |
|----|------------|
| build | `go -C worktrees/<NNN> build ./...` |
| unit | `go -C worktrees/<NNN> test ./...` |
| integration（PR-2+） | `go -C worktrees/<NNN> test -tags=integration ./adapters/mqtt/...` |
| lint | `golangci-lint run ./...`（0 issues） |
| archtest（PR-1） | `go -C worktrees/<NNN> test ./tools/archtest/...` |
| 覆盖率 | adapter ≥80%（CLAUDE.md） |
| commit message | 含 `ref: <framework> <file>` |
| PR body | `Refs: V11-4`, ship §6 模板 |

---

## 未决事项（明示不做）

| 项 | 不做理由 |
|----|---------|
| MQTT v3.1.1 兼容 | spec §3 OUT；用户已选 v5 only |
| 端到端 payload 加密 | spec §3 OUT；TLS 已覆盖 in-transit |
| EMQX 专属 conformance | Mosquitto 是 CI 基线；EMQX 兼容性留 backlog |
| codegen `transports: [amqp, mqtt]` 多 transport 派生 | 不在本 spec 范围；如需要由 cell 内显式多次 publish 替代（PR-5 内决策） |
| MDM / 零信任 cell 实施 | V11 ZT 系列另起 |
| iotdevice 切换原 HTTP/WebSocket 通道 | spec §3 OUT；保持原通道不变 |
