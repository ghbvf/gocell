# Tasks — Phase 4: Examples + Documentation

> 生成方式: 基于 spec.md (FR-1 ~ FR-10) + plan.md (Wave 0-4) + decisions.md
> 标记: [P] = 可与同 Wave 内其他 [P] 任务并行, [S] = 串行, [DEP:Txx] = 依赖任务 xx

---

## Wave 0: Tech-Debt 关闭 + 安全加固

### FR-7.1 RS256 默认化
- **T01** [S] `runtime/auth/keys.go`: 添加 `MustGenerateTestKeyPair() (*rsa.PrivateKey, *rsa.PublicKey)` helper。ref: Kubernetes service-account-token-key-generator
- **T02** [S][DEP:T01] `runtime/auth/jwt.go`: NewIssuer/NewVerifier 默认 RS256，无 RSA key pair 时 fail-fast 返回 error。保留 `WithHS256(secret)` 显式 Option。`WithSigningKey([]byte)` 标记 `// Deprecated`
- **T03** [S][DEP:T01] `runtime/auth/jwt_test.go`: 所有测试改用 MustGenerateTestKeyPair()

### FR-7.2 access-core RS256 切换
- **T04** [S][DEP:T02] `cells/access-core/cell.go`: 新增 `WithJWTIssuer(auth.JWTIssuer)` + `WithJWTVerifier(auth.JWTVerifier)` Option。`WithSigningKey([]byte)` 标记 Deprecated
- **T05** [S][DEP:T04] `cells/access-core/slices/sessionlogin/service.go`: 构造函数 signingKey []byte → auth.JWTIssuer 接口
- **T06** [P][DEP:T04] `cells/access-core/slices/sessionrefresh/service.go`: 同 T05
- **T07** [P][DEP:T04] `cells/access-core/slices/sessionvalidate/service.go`: 同 T05
- **T08** [S][DEP:T05,T06,T07] 更新 access-core 全部单元测试（预估 60+ 测试）使用 MustGenerateTestKeyPair()。验证: `go test ./cells/access-core/...` 全 PASS

### FR-7.3 outboxWriter fail-fast
- **T09** [S] `pkg/errcode/errcode.go`: 添加 `ERR_CELL_MISSING_OUTBOX` 哨兵码
- **T10** [S][DEP:T09] `cells/access-core/cell.go` Init: L2+ 校验 outboxWriter != nil，缺失返回 ERR_CELL_MISSING_OUTBOX
- **T11** [P][DEP:T09] `cells/audit-core/cell.go` Init: 同 T10
- **T12** [P][DEP:T09] `cells/config-core/cell.go` Init: 同 T10
- **T13** [S][DEP:T10,T11,T12] 更新 Cell 测试注入 noop outboxWriter（`outbox.WriterFunc` 或 mock）。验证: `go test ./cells/...` 全 PASS

### FR-7.4 S3 env prefix
- **T14** [P] `adapters/s3/client.go`: ConfigFromEnv 优先 GOCELL_S3_*，fallback S3_* + slog.Warn
- **T15** [P][DEP:T14] 更新 `.env.example` S3 变量前缀 + `adapters/s3/client_test.go` t.Setenv

### FR-8.2 + FR-8.3 Docker/Deprecated
- **T16** [P] `docker-compose.yml`: rabbitmq/minio 添加 start_period: 15s
- **T17** [P] `runtime/bootstrap/bootstrap.go`: WithEventBus 添加 // Deprecated 注释

### 基础设施
- **T18** [P] `go.mod`: 添加 `github.com/testcontainers/testcontainers-go` 依赖 + go mod tidy

---

## Wave 1: testcontainers 集成测试

### FR-6.2 PostgreSQL 集成测试
- **T19** [S][DEP:T18] `adapters/postgres/integration_test.go`: `//go:build integration` + testcontainers PostgreSQL 容器。测试: Pool 连接 + Health + Close。ref: pgx pgxpool_test
- **T20** [S][DEP:T19] TxManager 集成测试: RunInTx commit/rollback/panic/savepoint
- **T21** [S][DEP:T19] Migrator 集成测试: Up/Down/Status
- **T22** [S][DEP:T19] OutboxWriter 集成测试: 事务内写入 + context 无 tx fail-fast

### FR-6.3 Redis 集成测试
- **T23** [P][DEP:T18] `adapters/redis/integration_test.go`: `//go:build integration` + testcontainers Redis。测试: Client 连接 + Health + DistLock + IdempotencyChecker

### FR-6.4 RabbitMQ 集成测试
- **T24** [P][DEP:T18] `adapters/rabbitmq/integration_test.go`: `//go:build integration` + testcontainers RabbitMQ。测试: Connection + Publisher + Subscriber + ConsumerBase DLQ

### FR-6.5 Outbox 全链路
- **T25** [S][DEP:T22,T23,T24] `TestIntegration_OutboxFullChain`: business write + outbox write (同一 tx) → relay poll → RabbitMQ publish → consumer consume → idempotency check

### FR-6.6 覆盖率验证
- **T26** [S][DEP:T22] 验证 `go test -cover -tags=integration ./adapters/postgres/...` ≥ 80%

---

## Wave 2: 示例项目

### FR-2 todo-order（Wave 2a, P1 golden path）
- **T27** [S][DEP:T08,T13] `examples/todo-order/cells/order-cell/cell.yaml` + `slice.yaml` 元数据定义。运行 `gocell validate` 验证
- **T28** [S][DEP:T27] `examples/todo-order/cells/order-cell/cell.go`: OrderCell struct 实现 Cell 接口 + WithTxManager/WithOutboxWriter/WithPublisher Option
- **T29** [S][DEP:T28] `examples/todo-order/cells/order-cell/slices/order-create/`: handler + service + service_test.go
- **T30** [P][DEP:T28] `examples/todo-order/cells/order-cell/slices/order-query/`: handler + service
- **T31** [S][DEP:T28] `examples/todo-order/cells/order-cell/internal/domain/`: Order entity + Repository 接口
- **T32** [S][DEP:T31] `examples/todo-order/cells/order-cell/internal/adapters/postgres/`: Repository 实现
- **T33** [S][DEP:T27] `contracts/http/order/v1/contract.yaml` + `contracts/event/order-created/v1/contract.yaml`
- **T34** [S][DEP:T27] `journeys/J-order-create.yaml`
- **T35** [S][DEP:T29,T30,T32,T33] `examples/todo-order/main.go`: Assembly 配线（order-cell + postgres + rabbitmq adapter 注入）
- **T36** [S][DEP:T35] `examples/todo-order/docker-compose.yml` + `.env`
- **T37** [S][DEP:T36] `examples/todo-order/README.md`: 运行步骤 + curl 命令 + 事件消费验证步骤
- **T38** [S][DEP:T37] 验证: `cd examples/todo-order && go build .` 通过。ref: go-zero examples/ 项目结构

### FR-1 sso-bff（Wave 2b, P2）
- **T39** [P][DEP:T08,T13] `examples/sso-bff/main.go`: 注册 access-core + audit-core + config-core，注入 postgres.OutboxWriter + TxManager + redis.Client + rabbitmq.Publisher/Subscriber。ref: Kratos 认证中间件
- **T40** [S][DEP:T39] `examples/sso-bff/docker-compose.yml` + `.env`
- **T41** [S][DEP:T40] `examples/sso-bff/README.md`: curl 命令序列（login → me → refresh → config → logout → audit 查询）
- **T42** [S][DEP:T41] 验证: `cd examples/sso-bff && go build .` 通过

### FR-3 iot-device（Wave 2c, P2）
- **T43** [P][DEP:T08,T13] `examples/iot-device/cells/device-cell/cell.yaml`（type: edge, L4）+ slice.yaml × 3
- **T44** [S][DEP:T43] `examples/iot-device/cells/device-cell/cell.go`: DeviceCell struct（L4，不注入 outboxWriter）
- **T45** [S][DEP:T44] device-register + device-command + device-status slice 实现
- **T46** [S][DEP:T44] `examples/iot-device/cells/device-cell/internal/domain/`: Device + Command entity
- **T47** [S][DEP:T45,T46] `examples/iot-device/main.go`: Assembly 配线 + WebSocket hub
- **T48** [S][DEP:T47] `examples/iot-device/docker-compose.yml` + `README.md`（含 curl + wscat 命令）
- **T49** [S][DEP:T48] 验证: `cd examples/iot-device && go build .` 通过

---

## Wave 3: CI + 文档 + 模板

### FR-8.1 CI workflow
- **T50** [P][DEP:T18] `.github/workflows/ci.yml`: go build + test + vet + validate + grep internal check + kernel coverage gate。ref: Kratos .github/workflows
- **T51** [S][DEP:T50,T25] CI 集成测试 job: `go test -tags=integration ./adapters/...`（Docker 服务）

### FR-4 README
- **T52** [S][DEP:T38] `README.md`: 项目简介 + 架构 ASCII 图 + 核心概念（Cell/Slice/Contract/Assembly/Journey/L0-L4）
- **T53** [S][DEP:T52] README 快速开始: git clone + cd examples/todo-order + docker compose up -d + go run . + curl 验证
- **T54** [S][DEP:T53] README 30 分钟教程: 从零创建 Cell（≤ 15 步，每步有预期输出）
- **T55** [S][DEP:T54] README 示例索引 + 目录结构 + go get 集成指南

### FR-5 模板
- **T56** [P] `templates/adr.md`: MADR 格式 ADR 模板
- **T57** [P] `templates/cell-design.md`: Cell 设计文档模板
- **T58** [P] `templates/contract-review.md`: Contract 审查清单
- **T59** [P] `templates/runbook.md`: 运维手册模板。ref: Google SRE Runbook
- **T60** [P] `templates/postmortem.md`: 事故复盘模板
- **T61** [P] `templates/grafana-dashboard.json`: Prometheus-compatible placeholder dashboard

### FR-9 文档
- **T62** [S][DEP:T38,T42,T49] 示例 godoc: 3 个示例导出类型/函数注释
- **T63** [S] CHANGELOG.md Phase 4 更新
- **T64** [S] `docs/capability-inventory.md` Phase 4 更新 + v1.0 Scope Cut 附录
- **T65** [S] master-plan v1.0/v1.1 边界更新（7 kernel + 4 runtime + VictoriaMetrics → v1.1）

---

## Wave 4: 验证 + 收尾

### FR-10 测试
- **T66** [S][DEP:T38,T42,T49] `go build ./...` 全通过（含 examples/）
- **T67** [S][DEP:T66] `go test ./...` 全通过（不含 integration tag）
- **T68** [S][DEP:T67] `go vet ./...` 零警告
- **T69** [S][DEP:T68] `gocell validate` 零 error（含 examples/ 元数据）
- **T70** [S][DEP:T69] 分层 grep 验证: `grep -r "cells/.*/internal" examples/` = 0 匹配 + `grep -r "adapters/.*/internal" examples/` = 0 匹配（C-05 + C-06）
- **T71** [S][DEP:T67] kernel/ 覆盖率 >= 90% 验证
- **T72** [S][DEP:T67] postgres adapter 覆盖率 >= 80% 验证（含 integration tag）

### 测试编写 + E2E 验证
- **T73** [S][DEP:T55] 30 分钟 Gate E2E 验证: 按 README 教程从 clone 到 HTTP 200，记录测试编写步骤和实际耗时。contract test: todo-order 的 contract YAML 通过 gocell validate；journey test: J-order-create journey 定义验证
- **T74** [S][DEP:T73] Phase 4 收尾: phase-report + tech-debt 更新 + CHANGELOG 最终版

---

## Kernel Guardian 审查追加任务

（以下任务由 Kernel Guardian 在 S4.5 审查后追加，用于验证 kernel-constraints.md 中 C-01 ~ C-30 约束）

> 2026-04-06 第一轮审查: T75-T80 初始追加
> 2026-04-06 第二轮审查: 修正 T77/T78 约束编号映射错误，补充 T81-T88 覆盖遗漏约束

- **T75** [KG] 验证 kernel/ 零代码修改: `git diff develop -- src/kernel/ | grep "^[+-]" | grep -v "^[+-]\s*//"` 零行非注释修改（C-19）
- **T76** [KG] 验证分层隔离 6 项 grep 全部 0 匹配（C-01 ~ C-06）。具体命令见 kernel-constraints.md 表格，6 条 grep 全部执行
- **T77** [KG] 验证 examples/ 不 import `cells/.*/internal/` 且不 import `adapters/.*/internal/`（C-05 + C-06）。与 T70 交叉验证，确认 grep 覆盖两个方向
- **T78** [KG] 验证 todo-order cell.yaml/slice.yaml 通过 `gocell validate`，全部必填字段存在（C-07, C-08）
- **T79** [KG] 验证 iot-device L4 不注入 outboxWriter（C-13）
- **T80** [KG] 验证 testcontainers 全部 `//go:build integration`（C-14 关联，确保无 Docker 环境下 `go test ./...` 不失败）
- **T81** [KG][NEW] 验证 iot-device cell.yaml 含全部必填字段（C-09）: 对 `examples/iot-device/cells/device-cell/cell.yaml` 单独运行 `gocell validate`，确认 id/type/consistencyLevel/owner/schema.primary/verify.smoke 全部存在且 type=edge, consistencyLevel=L4
- **T82** [KG][NEW] 验证 contractUsages.role 拓扑合法性（C-10）: 检查 todo-order + iot-device 所有 slice.yaml 中 contractUsages.role 是否匹配 kind 对应合法角色（http->serve/call, event->publish/subscribe, command->handle/invoke）
- **T83** [KG][NEW] 验证 cell.type 枚举合规（C-11）: 确认 order-cell type=core, device-cell type=edge，值在 {core, edge, support} 范围内
- **T84** [KG][NEW] 验证 HS256 移除完整性（C-17）: `grep -rn "SigningMethodHS256" src/cells/access-core/` 零匹配（或仅出现在 test 文件 / Deprecated 标注路径中）。迁移后不得存在未标注的 HS256 默认路径
- **T85** [KG][NEW] 验证 sso-bff 使用 RSA key pair（C-18）: 代码审查 `examples/sso-bff/main.go`，确认使用 `auth.LoadRSAKeyPair` 或 `auth.NewIssuer(privateKey, ...)` 而非 `WithSigningKey([]byte(...))`
- **T86** [KG][NEW] 验证 kernel/cell.Cell + kernel/outbox 接口签名无变更（C-23, C-24）: `git diff develop -- src/kernel/cell/interfaces.go` 和 `git diff develop -- src/kernel/outbox/outbox.go` 均为零 diff 或仅注释变更
- **T87** [KG][NEW] 验证 iot-device contract YAML 注册到 contracts/ 目录（C-25）: 确认 `contracts/` 下存在 iot-device 相关契约定义（如 command.device.v1, event.device-status.v1 等）。注意: T33 仅创建 todo-order 契约，iot-device 契约需在 T43 或 T45 实施阶段同步创建
- **T88** [KG][NEW] 验证 examples/ 编码规范合规（C-28, C-29）: (1) `grep -rn "errors\.New" examples/` 零匹配（或仅 test 文件），确认使用 pkg/errcode；(2) `grep -rn "fmt\.Println\|log\.Printf" examples/` 零匹配，确认使用 slog

---

## 约束覆盖矩阵

| 约束 | 说明 | 覆盖任务（实施 + KG 验证） |
|------|------|--------------------------|
| C-01 | kernel/ no import runtime/adapters/cells/ | T76 |
| C-02 | cells/ no import adapters/ | T76 |
| C-03 | runtime/ no import adapters/cells/ | T76 |
| C-04 | adapters/ no import cells/ | T76 |
| C-05 | examples/ no import cells/*/internal/ | T70, T77 |
| C-06 | examples/ no import adapters/*/internal/ | T70(updated), T77 |
| C-07 | todo-order cell.yaml required fields | T27, T78 |
| C-08 | todo-order slice.yaml required fields | T27, T78 |
| C-09 | iot-device cell.yaml required fields | T43, **T81** |
| C-10 | contractUsages.role matches kind | T69, **T82** |
| C-11 | cell.type in {core, edge, support} | T69, **T83** |
| C-12 | L2 Cell outboxWriter fail-fast | T10, T11, T12, T13 |
| C-13 | L4 Cell no outboxWriter | T44, T79 |
| C-14 | outbox full chain testcontainers | T25, T80 |
| C-15 | postgres adapter coverage >= 80% | T26, T72 |
| C-16 | RS256 default, fail-fast | T02, T03 |
| C-17 | access-core no HS256 default path | T05-T08, **T84** |
| C-18 | sso-bff uses RSA key pair | T39, **T85** |
| C-19 | kernel/ zero code modification | T75 |
| C-20 | kernel/ coverage >= 90% | T71 |
| C-21 | kernel/ go vet zero warnings | T68 |
| C-22 | gocell validate zero error | T69 |
| C-23 | kernel/cell.Cell interface stable | T75, **T86** |
| C-24 | kernel/outbox interfaces stable | T75, **T86** |
| C-25 | contracts registered in contracts/ | T33(todo-order), **T87**(iot-device) |
| C-26 | adapters/ implement kernel/runtime interfaces | T66 |
| C-27 | S3 ConfigFromEnv GOCELL_S3_* | T14, T15 |
| C-28 | examples/ use errcode | **T88** |
| C-29 | examples/ use slog | **T88** |
| C-30 | WithEventBus Deprecated | T17 |

---

## 统计

- 总任务数: 88
- Wave 0 (tech-debt): T01-T18 (18 tasks)
- Wave 1 (testcontainers): T19-T26 (8 tasks)
- Wave 2 (examples): T27-T49 (23 tasks)
- Wave 3 (CI/docs/templates): T50-T65 (16 tasks)
- Wave 4 (验证): T66-T74 (9 tasks), 其中 T70 已更新增加 adapters/*/internal 检查
- KG 追加: T75-T88 (14 tasks, 含 8 条第二轮新增)

---

KG 审查确认: tasks.md 覆盖 kernel-constraints.md 全部 30 条约束。
