# iotdevice MQTT 发布演示路径

本文档说明 iotdevice 的可选 MQTT 发布通道：把 `device-registered` 事件外发到真实
MQTT v5 broker（`adapters/mqtt`），用于端到端演示该 adapter。设备 HTTP/WS 主路径
（注册 API、命令轮询）不受影响。

## 设计：tee fan-out（进程内 eventbus + 外部 MQTT mirror）

iotdevice 是 L4 DeviceLatent，默认用进程内 `eventbus` 发布 `device-registered`
事件。该事件在进程内由 `devicebootstrap` 订阅者**反应式消费**、入队 bootstrap 命令
（#1698 reactive loop）——所以默认通道有一个**真·进程内 sink**（contract 的
`actorSubscribers: [example-iot-platform]` 另是外部 actor 占位，与该进程内 sink 无关）。

当设置环境变量 `GOCELL_IOTDEVICE_MQTT_BROKERS` 时，组合根（`run.go`）把 cell 的
direct publisher 包成 `teePublisher`——事件**同时**投递到进程内 `eventbus`（继续喂
#1698 订阅者）**和**外部 MQTT broker（可用 `mosquitto_sub` 观测）。这是**并行 tee
双投递**（local eventbus + external MQTT mirror），不是单通道切换：进程内订阅者必须
继续收到事件，故 MQTT 是**叠加的外部观测 mirror**、不能替换默认通道。MQTT leg
fail-open（见下「broker 不可用时」），是 at-most-once 镜像。

> 对标：Watermill / go-micro / Kratos 用切换/配置 Publisher（DI）演示**单 sink** transport，
> 用 fan-out/tee 处理**真·多 sink**。本例因 #1698 引入进程内订阅者而存在真·多 sink
> （devicebootstrap + 外部 MQTT），故用 tee 而非 swap。
> ref: `ThreeDotsLabs/watermill` `message/decorator.go`（transform decorator）。

事件类型是点号形式 `event.device-registered.v1`；MQTT topic 是斜杠分层 + namespace
前缀。`mqttTopicPublisher` 把它映射为 `<ns>/event/device-registered/v1`（默认 ns =
`iotdevice` → `iotdevice/event/device-registered/v1`），payload（序列化后的 outbox
envelope）原样透传。

## 环境变量

| 变量 | 说明 | 默认 |
|------|------|------|
| `GOCELL_IOTDEVICE_MQTT_BROKERS` | 逗号分隔的 broker URL（如 `tcp://127.0.0.1:1883`）。**未设 = 通道关闭**，行为与今天逐字节一致 | 未设 |
| `GOCELL_IOTDEVICE_MQTT_TOPIC_NS` | topic namespace 前缀 | `iotdevice` |
| `GOCELL_IOTDEVICE_MQTT_CONNECT_DEADLINE` | bootstrap 初次连接等待预算（Go duration，如 `10s`）；broker 不可达时启动在此期限内 fail-fast 退出。格式错误或 ≤0 的覆盖值导致启动失败（不静默回退默认值） | `30s` |

**fail-fast，无 noop 回退**：通道开启后，配置/解析/连接错误会让启动失败并报错
（不静默降级到 noop publisher）。`Open` 阻塞等待首次连接，但受 `ConnectDeadline`
（默认 30s，可经 `GOCELL_IOTDEVICE_MQTT_CONNECT_DEADLINE` 覆盖）限定——broker 不可达
时启动在该期限内**报错退出**，而非随 root ctx 无限挂起（#1388）。ConnectionManager 仍
绑应用 lifecycle ctx，连接成功后不受该 deadline 影响（断线照常重连）。**建议先启动
broker 再跑 demo**；仅"未设环境变量"是合法的关闭方式。

**认证**：当前 demo 仅连接**无认证** broker（示例用 `mosquitto-no-auth.conf`）。未提供
用户名/密码环境变量；连接有认证的 broker 需扩展 `buildMQTTDirectPublisher`，在
`mqtt.NewConfig(...)` 调用里加一个 `mqtt.WithAuth(mqtt.AuthConfig{...})` 选项
（out-of-scope for this demo）。

## 跑通命令

```bash
# 1. 启动 MQTT broker（先于 demo）。最简：一次性 standalone broker（仅绑 127.0.0.1：
#    无认证 broker 不可暴露到宿主机外部网卡，仅供本机 demo）：
docker run --rm -p 127.0.0.1:1883:1883 eclipse-mosquitto:2.0 \
  mosquitto -c /mosquitto-no-auth.conf
# broker 现已是 iotdevice compose 服务；要随完整 demo 基建一起起（postgres + mosquitto），用：
#   GOCELL_EXAMPLE_POSTGRES_PASSWORD=<demo-pw> \
#     docker compose -f examples/iotdevice/docker-compose.yml up -d
#   注意：compose 对整个文件做插值，postgres 的必填 ${GOCELL_EXAMPLE_POSTGRES_PASSWORD:?}
#   使该命令在变量未设时直接报错退出（刻意如此——demo 的 postgres 存储本就需要该口令，
#   不放默认口令进仓库）。仅起 broker 用上面的 docker run，无需该变量。

# 2. 另开终端：订阅 device-registered 主题（外部观测者）
mosquitto_sub -h 127.0.0.1 -p 1883 -t 'iotdevice/#' -v

# 3. 启动 iotdevice，开启 MQTT 通道（in-memory 持久化 + MQTT 事件）
GOCELL_IOTDEVICE_MQTT_BROKERS=tcp://127.0.0.1:1883 \
  go run ./examples/iotdevice

# 4. 再开终端：注册一个设备（需要 RS256 bearer token，见 README.md）
curl -sS -X POST http://127.0.0.1:8083/api/v1/devices \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"sensor-1"}'
```

步骤 2 的 `mosquitto_sub` 会打印一条 `iotdevice/event/device-registered/v1` 消息，
body 是 outbox envelope（含 `eventId` 与 `payload`，payload 内 `name=sensor-1`、
`status=online`）。

## 可观测性

- `/readyz`（HealthListener，默认 `127.0.0.1:9093`）开启 MQTT 通道时包含
  `mqtt_ready` 探针，反映 broker 可达性（broker 断开 → 探针非 nil → readyz 降级）。
- 连接（`mqtt_ready` 探针 + disconnect）经 `bootstrap.WithManagedResource` 注册，Publisher（drain in-flight publishes）经 `bootstrap.WithManagedCloser` 注册；二者均参与框架 LIFO shutdown。

## broker 不可用时

L4 emitter 使用 `DirectPublishFailOpen`：注册请求本身**不因发布失败而失败**（设备已
持久化，事件丢失是运维跟进项，非请求失败）。运行期 broker 断开时 register 仍返回
201，`mqtt_ready` 转为不健康，事件不外发。

## 测试

- `examples/iotdevice/mqtt_test.go`：topic 映射单测。
- `examples/iotdevice/mqtt_smoke_test.go`：端到端 smoke（in-process mochi broker，
  真实 register → DirectEmitter → topic mapper → `mqtt.Publisher` → `mqtt.Subscriber`
  → 校验 payload），在普通 `go test ./examples/iotdevice/` 与 PR CI 中运行。
