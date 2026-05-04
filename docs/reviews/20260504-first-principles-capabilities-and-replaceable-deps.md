# GoCell 第一性原理：核心能力与可替换依赖

| 项 | 值 |
|---|---|
| 评估日期 | 2026-05-04 |
| 基准 commit | `11600a4f`（develop） |
| 评估视角 | 第一性原理 — 系统**必须**做什么、**用谁**做、哪些**可以替换** |
| 形态层 | GoCell 是编程框架（库 + 编译期工具），不拥有运行时与持久层 |

---

## 1. 能力清单（基于 goda 客观分析）

### 盘点方法

- 工具：`goda list "github.com/ghbvf/gocell/cmd/corebundle:all"` 输出 corebundle v1 默认二进制真实依赖的项目内包
- 总数：**项目内 Go 包 183 个**（含 examples 20 + 测试 helper ~13），其中 **corebundle 生产二进制依赖 102 包**
- 按"必需 / 可选 / 工具 / 测试 helper"四类组织，不再用主观"X 项能力"数字

### 公理集

- **A1**：让用户声明"我有什么 Cell / Slice / Contract"
- **A2**：让声明的东西能跑（init / start / stop）
- **A3**：跨 Cell 通信只通过 Contract
- **A4**：违反约束在编译期 / CI 期被拦截

---

### A. 必需层 — corebundle 生产二进制依赖（102 包）

#### A.1 kernel 层 18 包（公理核心实现）

| 子模块 | 公理对应 | 角色 |
|---|---|---|
| `cell` | A1 | Cell/Slice/Registry 接口与基类 |
| `metadata` | A1 + A4 | YAML 解析（cell.yaml / slice.yaml / contract.yaml / journey.yaml / assembly.yaml） |
| `wrapper` | A3 | ContractSpec 字面量 + Tracer 绑定 |
| `registry` | A2 | Cell + Contract in-memory 注册表 |
| `assembly` + `assembly/gentpl` | A2 | Cell 生命周期编排（FIFO/LIFO）+ main.go 模板 |
| `lifecycle` | A2 | ContextCloser + ManagedResource 协议 |
| `clock` | A2（可测试性） | 时钟接口（无 Real() 默认值，强制注入） |
| `governance` | A4 | REF/TOPO/VERIFY/FMT/ADV/OUTGARD 6 系列治理规则 |
| `verify` | A4 | Journey/smoke/unit/contract 4 prefix 解析 + go test 调度 |
| `depgraph` | A4 | 包级 typed 依赖图数据模型 |
| `outbox` | A3 + L2/L3 | Writer/Emitter/Relay/Publisher/Subscriber 接口 |
| `idempotency` | A3 | Claimer 接口（两阶段 Claim/Commit/Release） |
| `worker` | A2 | Worker 接口 + WorkerGroup |
| `crypto` | — | KeyProvider / ValueTransformer 接口 |
| `persistence` | — | TxRunner 接口 |
| `ctxkeys` | — | Cell-model context keys |
| `observability/metrics` | — | Provider/Collector 接口 |

#### A.2 runtime 层 22 包（公理运行时编排）

| 子模块 | 角色 |
|---|---|
| `bootstrap` | phase 0–10 启停编排（kernel/assembly 之上） |
| `auth` + `auth/config` + `auth/refresh` + `auth/refresh/memstore` | 完整 Auth 子系统（JWT / ServiceToken / Intent / Nonce / Authn/Authz / AuthPlan / Refresh / Mount） |
| `config` | YAML + env 配置加载（**注：默认热更新需 fsnotify，corebundle 链上不 import**） |
| `crypto` | kernel/crypto 的 type alias 包 |
| `shutdown` | SIGINT/SIGTERM + 排序 teardown |
| `worker` | WorkerGroup 生命周期管理 |
| `outbox` | Relay worker + Store 接口 |
| `eventbus` | in-memory Publisher/Subscriber（dev/test 替身） |
| `eventrouter` | Cell.Subscribe builder + EventRouter 生命周期 |
| `distlock` | Locker/Lock 接口（redis adapter 实现） |
| `devtools/catalog` | DevTools Catalog HTTP API + CLI export（J1 PR#357） |
| `http/router` | HTTP 路由（dual-mux primary/internal + AuthMeta + clock injection + tracer + policy coverage） |
| `http/middleware` | **19 个 middleware**：access_log / body_limit / cell_id / circuit_breaker / cookie_session / csrf / metrics / public_endpoints / rate_limit / real_ip / recorder / recovery / request_id / route_pattern / safe_observe / security_headers / trace_propagation / tracing / securecookie_bridge |
| `http/health` + `http/health/probequery` | Health probe 框架（Wrap + singleflight + verbose + readyz aggregator） |
| `http/devtools` | Catalog HTTP route 挂载点 |
| `observability/metrics` + `observability/poolstats` + `observability/tracing` | HTTP collector + 连接池统计 + Tracer 抽象 |

#### A.3 pkg 层 14 包（公理级共享工具）

| 包 | 角色 |
|---|---|
| `errcode` | **结构化错误码体系**（项目公理 — CLAUDE.md "错误必用 errcode"） |
| `contracts` | 共享 Go 类型（HTTPTransport / SchemaRefs / etc，contracts/ yaml 的 Go 镜像） |
| `query` | 分页 + 游标（ParsePageParamsOrWrite + MapPageResult + CursorCodec） |
| `httputil` | HTTP 响应封装 + ParseUUIDPathParam |
| `securecookie` | HMAC-SHA256 cookie |
| `aeadutil` + `secutil` | AEAD 加密辅助 + 安全工具 |
| `cmdrun` | 子进程白名单（avoid command injection） |
| `csvparam` | CSV 参数解析 |
| `ctxkeys` + `ctxcancel` | 通用 context keys + cancel 辅助 |
| `idutil` | UUID 生成（fail-fast 包装 crypto/rand） |
| `logutil` | slog 工具 |
| `validation` | 字段校验 |

#### A.4 adapters 层 5 包（corebundle 默认绑定的外部系统）

| 适配器 | 实现接口 | 外部依赖 |
|---|---|---|
| `postgres` | persistence.TxRunner + outbox.Writer + 域 Repository | `pgx/v5` + `goose/v3` |
| `redis` | distlock.Driver + idempotency.Claimer | `go-redis/v9` |
| `vault` | crypto.KeyProvider | `hashicorp/vault/api`（链巨大） |
| `prometheus` | observability/metrics.Provider | `prometheus/client_golang` |
| `adapterutil` | CloseWithDeadline 共享工具（不算独立 adapter） | 0 |

#### A.5 cells 层 36 包（v1 平台 Cell — **架构边界违例，详见软件工程视角报告**）

| Cell | Slice 数 | 内部包数 | 一致性等级 |
|---|---|---|---|
| `accesscore` | 10 (authorizationdecide / configreceive / identitymanage / rbacassign / rbaccheck / sessionlogin / sessionlogout / sessionrefresh / sessionvalidate / setup) + initialadmin + configgetter | 8 (adapters/http / adminprovision / domain / dto / mem / ports / sessionmint + cell 主) | L2 |
| `auditcore` | 4 (auditappend / auditarchive / auditquery / auditverify) | 5 (domain / dto / mem / ports + cell 主) | L2 |
| `configcore` | 6 (configpublish / configread / configsubscribe / configwrite / featureflag / flagwrite) | 7 (adapters/postgres / crypto / domain / dto / events / mem / ports + cell 主 + postgres) | L2 |

#### A.6 cmd 层 1 包

- `cmd/corebundle` — v1 默认 main 入口（24 个 .go 文件，按 PR-A66 flat + comment groups 组织）

---

### B. 可选层 — 按需启用（~16 包，不在 corebundle 默认链上）

| 类别 | 包 | 启用条件 |
|---|---|---|
| **kernel** | `command` + `command/commandtest` | 客户接入 L4 设备命令场景（如 IoT） |
| | `journey` | `gocell verify journey` CLI 调用（编译期工具） |
| | `scaffold` | `gocell scaffold` CLI 调用（编译期工具） |
| | `metadata/schemas` | embed.FS 模式校验（编译期工具） |
| | `outbox/outboxtest` | 测试 helper |
| | `cell/celltest` | 测试 helper |
| | `clock/clockmock` | 测试 helper |
| **runtime** | `command` | L4 设备命令运行时 SweeperLifecycle |
| | `websocket` | WebSocket 接入场景 |
| | `observability/logging` | slog 字段约定（**注：corebundle 实际未直接 import 该包，cells 各自直用 stdlib slog**） |
| **adapters** | `rabbitmq` | AMQP 消息总线 |
| | `oidc` | OIDC 身份联邦 |
| | `otel` | OpenTelemetry 追踪导出 |
| | `s3` | S3 对象存储 |
| | `circuitbreaker` | 第三方 CB 实现（可被自写替换，见 §2） |
| | `ratelimit` | 限流（自实现 token bucket，0 外部依赖） |
| | `websocket` | WebSocket 协议绑定（`coder/websocket`） |

---

### C. 工具链 / 治理（13 Go 包 + 非 Go 资源）

#### C.1 cmd/gocell CLI 工具链（4 包）

- `cmd/gocell` — 入口
- `cmd/gocell/app` — 8 子命令分发：`validate` / `scaffold` / `generate` / `check` / `verify` / `graph` / `export` / `dispatch`
- `cmd/gocell/app/printers` — 输出格式（text / json / sarif）

#### C.2 tools/* 9 包（CI / 编译期工具）

| 工具 | 角色 |
|---|---|
| `archtest` | **158 条**架构守卫规则（LAYER-01..10 / AUTH-PLAN-04 / OUTBOX-SERVICE-01 / HANDLER-RECEIPT-WRITE-01 / CTXSOURCE / 等）|
| `codegen` + `codegen/cellgen` + `codegen/contractgen` | 通用 codegen framework + cell.go 派生 + contract DTO/iface 派生（K#04 PR-1 ship）|
| `depgraph` | go/packages → kernel/depgraph.Graph 构图（J1 同窗口） |
| `e2egate` + `e2egate/cmd/e2egate` | 端到端测试网关 |
| `generatedcatalog` + `generatedverify` | codegen 产物校验 gate |
| `metricschema` | metrics schema 校验（OBS-01 typed gate） |

#### C.3 非 Go 资源

- `contracts/` — 49 yaml schemas（HTTP 33 + event 16）
- `journeys/` — 8 J-*.yaml + status-board.yaml
- `assemblies/` — 1 corebundle assembly.yaml + boundary.yaml + metrics-schema.yaml
- `actors.yaml` — 4 外部 actor 注册
- `.github/workflows/` — 9 个 workflow（ci / pr-check / _build-lint / governance / test-race / security-static / security-vuln / otel-collector-nightly / qodana）
- `Makefile` — 13 个 target
- `.golangci.yml` — 21 linters + depguard 8 条 isolation
- `hack/verify-*.sh` — verify 脚本套件（含 verify-shellcheck / verify-codegen-cell / verify-test-time-literal / verify-archtest 等）

---

### D. 测试 helper（~13 包，仅 _test.go 用）

`pkg/contracttest` / `pkg/scaffoldfs` / `pkg/testutil/sloghelper` / `pkg/testutil/testtime`
`kernel/cell/celltest` / `kernel/clock/clockmock` / `kernel/command/commandtest` / `kernel/outbox/outboxtest` / `kernel/metadata/schemas`
`runtime/auth/authtest` / `runtime/auth/refresh/storetest` / `runtime/distlock/locktest` / `runtime/http/healthtest` / `runtime/outbox/outboxtest`

---

### 包级总账

| 类别 | Go 包数 | 备注 |
|---|---|---|
| **A. 必需层（corebundle 依赖）** | 102 | kernel 18 + runtime 22 + pkg 14 + adapters 5 + cells 36 + cmd 1 + 辅助 6 |
| **B. 可选层** | ~16 | 按需启用的 kernel/runtime/adapters |
| **C. 工具链** | 13 Go + 非 Go 资源 | cmd/gocell 4 + tools/* 9 + workflows + Makefile + scripts |
| **D. 测试 helper** | ~13 | 仅 _test.go 用 |
| **examples（参考实现）** | 20 | demo / iotdevice / ssobff / todoorder |
| **项目总包**（go list ./...） | **183** | — |

### 外部依赖最小集（仅 corebundle 必需）

去掉所有可选 adapter，corebundle v1 默认运行需要：
- **stdlib + `gopkg.in/yaml.v3` + `santhosh-tekuri/jsonschema/v6`**（kernel/metadata + governance）
- **`golang.org/x/tools`**（archtest / codegen 仅编译期）
- **`golang.org/x/crypto`**（auth + securecookie）
- **`golang-jwt/jwt/v5`**（runtime/auth）
- **`go-chi/chi/v5`**（HTTP 路由，可替换见 §2）
- **`pgx/v5`** + **`goose/v3`**（postgres adapter）
- **`go-redis/v9`**（redis adapter）
- **`hashicorp/vault/api`**（vault adapter，链巨大）
- **`prometheus/client_golang`**（metrics adapter）
- **`google/uuid`**（idutil）
- **`felixge/httpsnoop`**（HTTP middleware，可替换见 §2）

约 11 个非 stdlib 直接依赖即可跑 corebundle 默认平台。

---

## 2. 可替换清单

仅列 **ROI 高 + 自实现风险低**的三项。每项都是"加分项依赖"——替换后**仅减少 vendor**，不影响功能。

### 2.1 `go-chi/chi/v5` → Go 1.22+ `net/http.ServeMux`

| 项 | 内容 |
|---|---|
| **现状** | 7 处直接 import；`runtime/http/router/` + `runtime/http/middleware/` 主要消费 |
| **替换可行性** | Go 1.22+ `net/http.ServeMux` 已支持 `GET /api/v1/users/{id}` 模式匹配 + method routing |
| **自实现复杂度** | 中（middleware chain ~150 LOC；项目已有 middleware 结构齐全，路由部分换 ServeMux） |
| **影响面** | 30–50 处 handler 中的 `chi.URLParam` 调用点改用 `r.PathValue("id")` |
| **建议时机** | K#04 PR-4 codegen 全量迁移完成后（cellgen 模板可一次性切到 stdlib ServeMux） |
| **风险** | 低（无安全敏感，纯协议路由） |

### 2.2 `sony/gobreaker/v2` → 自写

| 项 | 内容 |
|---|---|
| **现状** | 仅 `adapters/circuitbreaker/` 一处使用 |
| **自实现复杂度** | 小（状态机 closed/open/half-open + counter，~150 LOC） |
| **下沉位置** | `pkg/circuitbreaker/`（leaf 包） |
| **影响面** | 1 个 adapter 文件 + 几个 cell 注入点 |
| **建议时机** | 任意 PR 路过该 adapter 时搭车修 |
| **风险** | 低（业务弹性逻辑，无安全敏感） |

### 2.3 `felixge/httpsnoop` → 自写

| 项 | 内容 |
|---|---|
| **现状** | `runtime/http/middleware/` 内用作 `http.ResponseWriter` wrapper（捕获 status code / bytes written） |
| **自实现复杂度** | 小（ResponseWriter wrap + WriteHeader hook，~80 LOC，注意 `http.Hijacker` / `http.Flusher` 接口委托） |
| **下沉位置** | `pkg/httputil/`（已存在该包） |
| **影响面** | middleware 内一处替换 |
| **建议时机** | 任意 middleware 类 PR 路过时搭车修 |
| **风险** | 低（协议层包装，无安全敏感） |

---

> 报告结束。
