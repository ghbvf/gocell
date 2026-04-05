# PR Plan — Phase 4: Examples + Documentation

> 生成方式: 基于 tasks.md (T01-T88) + task-dependency-analysis.md
> 生成日期: 2026-04-06
> 分析人: 项目经理 Agent

---

## 划分原则

1. 每个 PR 可独立 `go build ./...`（或独立子目录 build）
2. 每个 PR 可独立 `go test ./...`（或独立子目录 test）
3. 契约先行: 含 API/接口变更的 PR 遵循 contract → test → implement 顺序
4. PR 粒度: 同一模块内强耦合任务合并为一个 PR，跨模块解耦任务拆分
5. PR 尺寸: 每个 PR 估计新增/修改文件 ≤ 20 个，避免超大 PR 难以审查
6. 分层约束: PR 内不得引入跨层 import（C-01 ~ C-06）

---

## PR 总览表

| PR | scope | tasks | depends | status |
|----|-------|-------|---------|--------|
| PR-01 | runtime/auth RS256 + key helper | T01, T02, T03 | - | merged |
| PR-02 | access-core RS256 切换 | T04, T05, T06, T07, T08 | PR-01 | merged |
| PR-03 | outboxWriter fail-fast + errcode | T09, T10, T11, T12, T13 | - | merged |
| PR-04 | S3 env prefix + docker infra | T14, T15, T16, T17 | - | merged |
| PR-05 | testcontainers go.mod | T18 | - | merged |
| PR-06 | postgres integration tests | T19, T20, T21, T22, T26 | PR-05 | merged |
| PR-07 | redis + rabbitmq integration tests | T23, T24 | PR-05 | merged |
| PR-08 | outbox full chain integration | T25 | PR-06, PR-07 | merged |
| PR-09 | todo-order example | T27-T38 | PR-02, PR-03 | merged |
| PR-10 | sso-bff example | T39-T42 | PR-02, PR-03 | merged |
| PR-11 | iot-device example | T43-T49 | PR-02, PR-03 | merged |
| PR-12 | CI workflow | T50, T51 | PR-05, PR-08 | merged |
| PR-13 | README getting started | T52-T55 | PR-09 | merged |
| PR-14 | project templates | T56-T61 | - | merged |
| PR-15 | docs (YAML fixes + validation) | T62-T65 | PR-09, PR-10, PR-11 | merged |
| PR-16 | final validation + gate | T66-T74 | all | merged |
| PR-17 | KG verification | T75-T88 | PR-16 | merged |

---

## 详细 PR 说明

### Wave 0 PR 组（可并行: PR-01、PR-03、PR-04、PR-05 无相互依赖）

---

#### PR-01: runtime/auth RS256 + key helper

**Wave:** 0
**Scope:** `runtime/auth/`
**Tasks:** T01, T02, T03
**Depends:** 无

**文件清单（预估）:**
- `runtime/auth/keys.go` — 新增 `MustGenerateTestKeyPair()`
- `runtime/auth/jwt.go` — NewIssuer/NewVerifier 默认 RS256，`WithHS256(secret)` 保留，`WithSigningKey([]byte)` 标记 `// Deprecated`
- `runtime/auth/jwt_test.go` — 全部测试改用 MustGenerateTestKeyPair()

**契约约束:** kernel/cell 接口签名不得修改（C-23）；runtime/auth 不 import cells/ 或 adapters/（C-03）

**verify 命令:**
```bash
go build ./runtime/auth/...
go test ./runtime/auth/...
go vet ./runtime/auth/...
```

**P1 验收标准:**
- `NewIssuer(nil, nil)` 返回 error（fail-fast，非降级 HS256）
- `MustGenerateTestKeyPair()` 返回有效 2048-bit RSA key pair
- `WithSigningKey([]byte)` 编译可用，注释含 `// Deprecated`
- `go test ./runtime/auth/...` 全 PASS

**branch:** `phase-4/pr-01-auth-rs256`

---

#### PR-02: access-core RS256 切换

**Wave:** 0（依赖 PR-01）
**Scope:** `cells/access-core/`
**Tasks:** T04, T05, T06, T07, T08
**Depends:** PR-01

**文件清单（预估）:**
- `cells/access-core/cell.go` — 新增 `WithJWTIssuer(auth.JWTIssuer)` + `WithJWTVerifier(auth.JWTVerifier)` Option；`WithSigningKey([]byte)` 标记 Deprecated
- `cells/access-core/slices/sessionlogin/service.go` — 构造函数 `signingKey []byte` → `auth.JWTIssuer` 接口
- `cells/access-core/slices/sessionrefresh/service.go` — 同上
- `cells/access-core/slices/sessionvalidate/service.go` — 同上（含 `auth.JWTVerifier`）
- `cells/access-core/**/*_test.go` — 所有测试改用 MustGenerateTestKeyPair()（预估 60+ 测试）

**契约约束:** cells/ 不 import adapters/（C-02）；kernel/cell.Cell 接口签名不得修改（C-23）

**verify 命令:**
```bash
go build ./cells/access-core/...
go test ./cells/access-core/...
go vet ./cells/access-core/...
grep -rn "SigningMethodHS256" cells/access-core/  # 期望 0 匹配（C-17）
```

**P1 验收标准:**
- `go test ./cells/access-core/...` 全 PASS（预估 60+ 用例）
- `grep -rn "SigningMethodHS256" cells/access-core/` 返回 0 匹配（或仅含 Deprecated 注释行）
- sessionlogin/sessionrefresh/sessionvalidate 构造函数参数不含 `[]byte signingKey`

**branch:** `phase-4/pr-02-access-rs256`

---

#### PR-03: outboxWriter fail-fast + errcode

**Wave:** 0（独立，可与 PR-01 并行）
**Scope:** `pkg/errcode/`, `cells/access-core/`, `cells/audit-core/`, `cells/config-core/`
**Tasks:** T09, T10, T11, T12, T13
**Depends:** 无（PR-02 可并行推进，T10 依赖 T09 但 T09 不依赖 PR-01）

注意：T10 依赖 cells/access-core/cell.go，与 PR-02 同文件。PR-03 合并时须基于 PR-02 分支，或 PR-02/PR-03 合并到同一 PR（如冲突风险高则合并为一个 PR）。

**文件清单（预估）:**
- `pkg/errcode/errcode.go` — 新增 `ERR_CELL_MISSING_OUTBOX` 哨兵码
- `cells/access-core/cell.go` — Init: L2+ 校验 outboxWriter != nil（依赖 T09）
- `cells/audit-core/cell.go` — 同上
- `cells/config-core/cell.go` — 同上
- `cells/access-core/**/*_test.go` + `cells/audit-core/**/*_test.go` + `cells/config-core/**/*_test.go` — 注入 noop outboxWriter（outbox.WriterFunc 或 pkg/testutil.NoopWriter）

**契约约束:** BaseCell.Init 方法签名不得修改（C-23）；校验逻辑在 Cell.Init 中，不修改 kernel/（C-19）

**verify 命令:**
```bash
go build ./pkg/errcode/... ./cells/...
go test ./cells/...
```

**P1 验收标准:**
- L2 Cell Init 缺 outboxWriter 时返回 `ERR_CELL_MISSING_OUTBOX` 错误
- `go test ./cells/...` 全 PASS
- `go test ./pkg/errcode/...` 全 PASS

**branch:** `phase-4/pr-03-outbox-failfast`

注意：若 PR-02 和 PR-03 在 `cells/access-core/cell.go` 产生冲突，可将 T04/T10 合并到同一 PR，统一修改 `cell.go`。按 task-dependency-analysis.md 文件冲突分析原则，同文件的修改须串行处理。

---

#### PR-04: S3 env prefix + docker infra

**Wave:** 0（独立，可与 PR-01/PR-02/PR-03 并行）
**Scope:** `adapters/s3/`, `docker-compose.yml`, `runtime/bootstrap/`
**Tasks:** T14, T15, T16, T17
**Depends:** 无

**文件清单（预估）:**
- `adapters/s3/client.go` — `ConfigFromEnv` 优先 `GOCELL_S3_*`，fallback `S3_*` + `slog.Warn`
- `adapters/s3/client_test.go` — `t.Setenv("GOCELL_S3_ENDPOINT", ...)` 更新
- `.env.example` — S3 变量改为 `GOCELL_S3_*` 前缀
- `docker-compose.yml` — rabbitmq/minio 服务添加 `start_period: 15s`
- `runtime/bootstrap/bootstrap.go` — `WithEventBus` 添加 `// Deprecated` 注释

**契约约束:** adapters/ 不 import cells/（C-04）；`GOCELL_S3_*` 前缀必须生效（C-27）；`WithEventBus` 标注 Deprecated（C-30）

**verify 命令:**
```bash
go build ./adapters/s3/... ./runtime/bootstrap/...
go test ./adapters/s3/...
grep "GOCELL_S3_" adapters/s3/client.go  # 期望有匹配
```

**P1 验收标准:**
- `os.Getenv("GOCELL_S3_ENDPOINT")` 优先于 `os.Getenv("S3_ENDPOINT")`
- fallback 时有 `slog.Warn` 日志
- `go test ./adapters/s3/...` 全 PASS
- `WithEventBus` 注释含 `// Deprecated`

**branch:** `phase-4/pr-04-s3-env-infra`

---

#### PR-05: testcontainers go.mod

**Wave:** 0（独立，可与 PR-01/PR-02/PR-03 并行）
**Scope:** `go.mod`, `go.sum`
**Tasks:** T18
**Depends:** 无

**文件清单:**
- `go.mod` — 添加 `github.com/testcontainers/testcontainers-go` 依赖
- `go.sum` — go mod tidy 后更新

**verify 命令:**
```bash
go build ./...
go mod tidy
```

**P1 验收标准:**
- `go build ./...` 全通过（无编译错误）
- `go.mod` 中 testcontainers-go 版本锁定
- 不影响无 `//go:build integration` 的常规 test 运行

**branch:** `phase-4/pr-05-testcontainers-dep`

---

### Wave 1 PR 组（PR-06 和 PR-07 可并行；PR-08 需等 PR-06 + PR-07）

---

#### PR-06: postgres integration tests

**Wave:** 1
**Scope:** `adapters/postgres/`
**Tasks:** T19, T20, T21, T22, T26
**Depends:** PR-05

**文件清单（预估）:**
- `adapters/postgres/integration_test.go` — `//go:build integration`；testcontainers PostgreSQL 容器；Pool 连接/Health/Close
- `adapters/postgres/txmanager_integration_test.go` — RunInTx commit/rollback/panic/savepoint
- `adapters/postgres/migrator_integration_test.go` — Up/Down/Status
- `adapters/postgres/outboxwriter_integration_test.go` — 事务内写入 + context 无 tx fail-fast

**契约约束:** 所有集成测试文件第一行必须为 `//go:build integration`（C-14 关联，KG-04）；不修改 kernel/（C-19）

**verify 命令:**
```bash
go build ./adapters/postgres/...
go test -tags=integration -v ./adapters/postgres/...
go test -tags=integration -cover ./adapters/postgres/... | grep -E "coverage:"  # 期望 >=80%
```

**P1 验收标准:**
- `go test ./adapters/postgres/...`（无 integration tag）PASS（不启动 Docker）
- `go test -tags=integration ./adapters/postgres/...` PASS（需 Docker）
- postgres adapter 覆盖率 ≥ 80%（C-15）

**branch:** `phase-4/pr-06-postgres-integration`

---

#### PR-07: redis + rabbitmq integration tests

**Wave:** 1（可与 PR-06 并行）
**Scope:** `adapters/redis/`, `adapters/rabbitmq/`
**Tasks:** T23, T24
**Depends:** PR-05

**文件清单（预估）:**
- `adapters/redis/integration_test.go` — `//go:build integration`；testcontainers Redis；Client/Health/DistLock/IdempotencyChecker
- `adapters/rabbitmq/integration_test.go` — `//go:build integration`；testcontainers RabbitMQ；Connection/Publisher/Subscriber/ConsumerBase DLQ

**契约约束:** 同 PR-06，所有集成测试文件须有 build tag

**verify 命令:**
```bash
go build ./adapters/redis/... ./adapters/rabbitmq/...
go test -tags=integration -v ./adapters/redis/...
go test -tags=integration -v ./adapters/rabbitmq/...
```

**P1 验收标准:**
- 无 tag 时 `go test ./adapters/redis/... ./adapters/rabbitmq/...` PASS（不启动 Docker）
- 带 tag 时 testcontainers 连接、健康检查、DLQ 路由测试全 PASS

**branch:** `phase-4/pr-07-redis-rabbitmq-integration`

---

#### PR-08: outbox full chain integration

**Wave:** 1
**Scope:** `adapters/` (跨 postgres/redis/rabbitmq)
**Tasks:** T25
**Depends:** PR-06, PR-07

**文件清单（预估）:**
- `adapters/outbox_fullchain_integration_test.go` — `//go:build integration`；`TestIntegration_OutboxFullChain`：business write + outbox write（同一 tx）→ relay poll → RabbitMQ publish → consumer consume → idempotency check

**契约约束:** 测试 build tag 隔离（C-14）；不修改 kernel/（C-19）

**verify 命令:**
```bash
go test -tags=integration -run TestIntegration_OutboxFullChain -v ./adapters/...
```

**P1 验收标准:**
- `TestIntegration_OutboxFullChain` PASS（需 Docker）
- 全链路覆盖: write → relay → publish → consume → idempotency 五个环节均有断言

**branch:** `phase-4/pr-08-outbox-fullchain`

---

### Wave 2 PR 组（PR-09、PR-10、PR-11 可并行；同依赖 PR-02 AND PR-03）

---

#### PR-09: todo-order example

**Wave:** 2a（P1 golden path）
**Scope:** `examples/todo-order/`, `contracts/http/order/v1/`, `contracts/event/order-created/v1/`, `journeys/`
**Tasks:** T27, T28, T29, T30, T31, T32, T33, T34, T35, T36, T37, T38
**Depends:** PR-02 AND PR-03

**文件清单（预估）:**
- `examples/todo-order/cells/order-cell/cell.yaml`
- `examples/todo-order/cells/order-cell/slices/order-create/slice.yaml`
- `examples/todo-order/cells/order-cell/slices/order-query/slice.yaml`
- `examples/todo-order/cells/order-cell/cell.go`
- `examples/todo-order/cells/order-cell/slices/order-create/handler.go`
- `examples/todo-order/cells/order-cell/slices/order-create/service.go`
- `examples/todo-order/cells/order-cell/slices/order-create/service_test.go`
- `examples/todo-order/cells/order-cell/slices/order-query/handler.go`
- `examples/todo-order/cells/order-cell/slices/order-query/service.go`
- `examples/todo-order/cells/order-cell/internal/domain/order.go`
- `examples/todo-order/cells/order-cell/internal/adapters/postgres/repository.go`
- `contracts/http/order/v1/contract.yaml`
- `contracts/event/order-created/v1/contract.yaml`
- `journeys/J-order-create.yaml`
- `examples/todo-order/main.go`
- `examples/todo-order/docker-compose.yml`
- `examples/todo-order/.env`
- `examples/todo-order/README.md`

**契约约束:**
- `examples/todo-order/` 不 import `cells/*/internal/` 或 `adapters/*/internal/`（C-05, C-06）
- cell.yaml 含全部必填字段（C-07）；slice.yaml 含全部必填字段（C-08）
- 不使用 `errors.New` 裸暴露（C-28）；使用 slog（C-29）
- contract YAML 注册到 `contracts/` 目录（C-25）

**verify 命令:**
```bash
cd examples/todo-order && go build .
go test ./examples/todo-order/...
gocell validate --root examples/todo-order  # 或等效
grep -r "cells/.*/internal" examples/todo-order/  # 期望 0 匹配
grep -r "adapters/.*/internal" examples/todo-order/  # 期望 0 匹配
grep -rn "errors\.New" examples/todo-order/  # 期望 0 匹配（或仅 test 文件）
grep -rn "fmt\.Println\|log\.Printf" examples/todo-order/  # 期望 0 匹配
```

**P1 验收标准:**
- `go build .` 通过
- `gocell validate` 对 order-cell 元数据零 error（C-07/C-08）
- 分层 grep 全部 0 匹配（C-05/C-06）
- README 含 curl 命令序列和事件消费验证步骤

**branch:** `phase-4/pr-09-example-todo-order`

---

#### PR-10: sso-bff example

**Wave:** 2b（P2，可与 PR-09/PR-11 并行）
**Scope:** `examples/sso-bff/`
**Tasks:** T39, T40, T41, T42
**Depends:** PR-02 AND PR-03

**文件清单（预估）:**
- `examples/sso-bff/main.go`（Assembly 配线：access-core + audit-core + config-core + postgres.OutboxWriter + TxManager + redis.Client + rabbitmq.Publisher/Subscriber）
- `examples/sso-bff/docker-compose.yml`
- `examples/sso-bff/.env`
- `examples/sso-bff/README.md`

**契约约束:**
- sso-bff main.go 使用 `auth.LoadRSAKeyPair` 或 `auth.NewIssuer(privateKey, ...)`，不使用 `WithSigningKey([]byte)`（C-18）
- `postgres.NewOutboxWriter(pool)` 注入到 access-core/audit-core/config-core（KG-05）
- 不 import `cells/*/internal/` 或 `adapters/*/internal/`（C-05, C-06）
- 使用 slog（C-29）；使用 errcode（C-28）

**verify 命令:**
```bash
cd examples/sso-bff && go build .
grep -r "cells/.*/internal" examples/sso-bff/  # 期望 0 匹配
grep -r "adapters/.*/internal" examples/sso-bff/  # 期望 0 匹配
grep "WithSigningKey" examples/sso-bff/main.go  # 期望 0 匹配（RSA 模式验证）
```

**P1 验收标准:**
- `go build .` 通过
- main.go 使用 RSA key pair（C-18），不含 `WithSigningKey([]byte(...))`
- 分层 grep 全部 0 匹配（C-05/C-06）
- README 含完整 curl 序列（login → me → refresh → config → logout → audit 查询）

**branch:** `phase-4/pr-10-example-sso-bff`

---

#### PR-11: iot-device example

**Wave:** 2c（P2，可与 PR-09/PR-10 并行）
**Scope:** `examples/iot-device/`, `contracts/`（iot-device 契约）
**Tasks:** T43, T44, T45, T46, T47, T48, T49
**Depends:** PR-02 AND PR-03

**文件清单（预估）:**
- `examples/iot-device/cells/device-cell/cell.yaml`（type: edge, consistencyLevel: L4）
- `examples/iot-device/cells/device-cell/slices/device-register/slice.yaml`
- `examples/iot-device/cells/device-cell/slices/device-command/slice.yaml`
- `examples/iot-device/cells/device-cell/slices/device-status/slice.yaml`
- `examples/iot-device/cells/device-cell/cell.go`（L4，不注入 outboxWriter）
- `examples/iot-device/cells/device-cell/slices/device-register/` （handler + service）
- `examples/iot-device/cells/device-cell/slices/device-command/` （handler + service）
- `examples/iot-device/cells/device-cell/slices/device-status/` （handler + service）
- `examples/iot-device/cells/device-cell/internal/domain/device.go`
- `examples/iot-device/cells/device-cell/internal/domain/command.go`
- `examples/iot-device/main.go`（WebSocket hub 集成）
- `examples/iot-device/docker-compose.yml`
- `examples/iot-device/README.md`
- `contracts/command/device/v1/contract.yaml`（iot-device 契约，T87 依赖）
- `contracts/event/device-status/v1/contract.yaml`

**契约约束:**
- device-cell L4 不注入 outboxWriter（C-13）
- cell.yaml type=edge, consistencyLevel=L4，含全部必填字段（C-09/C-11）
- contractUsages.role 匹配 kind 合法角色（C-10）
- iot-device 相关 contract 注册到 `contracts/` 目录（C-25）
- 不 import `cells/*/internal/` 或 `adapters/*/internal/`（C-05, C-06）

**verify 命令:**
```bash
cd examples/iot-device && go build .
gocell validate --root examples/iot-device
grep -r "outboxWriter" examples/iot-device/cells/device-cell/cell.go  # 期望 0 匹配（C-13）
grep -r "cells/.*/internal" examples/iot-device/  # 期望 0 匹配
grep -r "adapters/.*/internal" examples/iot-device/  # 期望 0 匹配
```

**P1 验收标准:**
- `go build .` 通过
- device-cell Init 不含 outboxWriter 校验（C-13）
- `gocell validate` 零 error（cell.yaml/slice.yaml 全必填字段）（C-09）
- contracts/ 下有 iot-device 相关契约定义（C-25）
- README 含 curl + wscat 命令

**branch:** `phase-4/pr-11-example-iot-device`

---

### Wave 3 PR 组（PR-13、PR-14 可并行；PR-12 依赖 PR-08）

---

#### PR-12: CI workflow

**Wave:** 3
**Scope:** `.github/workflows/`
**Tasks:** T50, T51
**Depends:** PR-05（testcontainers dep），PR-08（outbox 全链路集成完成，证明 integration job 可跑）

**文件清单（预估）:**
- `.github/workflows/ci.yml` — job `test`: go build + test + vet + validate + grep internal check + kernel coverage gate；job `integration`: `-tags=integration` + Docker services

**契约约束:**
- CI grep 检查覆盖 `cells/.*/internal` 和 `adapters/.*/internal`（C-03/KG-03）
- kernel/ 覆盖率 ≥ 90% 作为 CI 失败条件（C-20/KG-10）
- integration job 使用 `runs-on: ubuntu-latest`（原生 Docker，KG-04 缓解方案）
- `gocell validate` 步骤对 examples/ 单独运行（KG-08 方案 2，不修改 kernel/）

**verify 命令:**
```bash
# 本地用 act 工具验证（可选）；或提交后检查 GitHub Actions 运行结果
# 手动验证关键步骤:
grep -r "cells/.*/internal" examples/  # CI 检查逻辑验证
grep -r "adapters/.*/internal" examples/
go test -cover ./kernel/... | tail -5
```

**P1 验收标准:**
- CI workflow 语法有效（`act --list` 无报错）
- 包含两个 job: `test` 和 `integration`
- kernel/ 覆盖率检查作为 CI 失败条件
- grep 分层检查纳入 `test` job

**branch:** `phase-4/pr-12-ci-workflow`

---

#### PR-13: README getting started

**Wave:** 3（依赖 PR-09 go build 通过）
**Scope:** `README.md`
**Tasks:** T52, T53, T54, T55
**Depends:** PR-09（todo-order 可运行，提供 curl 验证步骤基础）

**文件清单:**
- `README.md`（全量重写或大幅更新）

**内容要求（T52-T55）:**
- T52: 项目简介 + 架构 ASCII 图 + 核心概念（Cell/Slice/Contract/Assembly/Journey/L0-L4）
- T53: 快速开始: `git clone` → `cd examples/todo-order` → `docker compose up -d` → `go run .` → `curl` 验证（每步有预期输出）
- T54: 30 分钟教程: 从零创建 Cell（≤ 15 步，每步有预期输出）
- T55: 示例索引 + 目录结构 + go get 集成指南

**verify 命令:**
```bash
# 文档审查，无 go build 需求
# 验证 README 中的命令在 todo-order 目录实际可执行
cd examples/todo-order && go build .  # 证明 README 快速开始路径有效
```

**P1 验收标准:**
- 30 分钟 Gate 教程 ≤ 15 步，每步有预期输出
- 快速开始路径与 PR-09 todo-order 实际可运行状态一致
- 架构 ASCII 图覆盖 kernel/cells/runtime/adapters/examples 六层

**branch:** `phase-4/pr-13-readme`

---

#### PR-14: project templates

**Wave:** 3（无前置依赖，可最早启动）
**Scope:** `templates/`
**Tasks:** T56, T57, T58, T59, T60, T61
**Depends:** 无（完全独立，可在 Wave 0 期间并行完成）

**文件清单:**
- `templates/adr.md` — MADR 格式 ADR 模板
- `templates/cell-design.md` — Cell 设计文档模板
- `templates/contract-review.md` — Contract 审查清单
- `templates/runbook.md` — 运维手册模板（ref: Google SRE Runbook）
- `templates/postmortem.md` — 事故复盘模板
- `templates/grafana-dashboard.json` — Prometheus-compatible placeholder dashboard

**verify 命令:**
```bash
ls templates/  # 确认 6 个文件均存在
# grafana JSON 格式验证
python3 -c "import json; json.load(open('templates/grafana-dashboard.json'))"
```

**P1 验收标准:**
- 6 个模板文件全部存在
- grafana-dashboard.json 是合法 JSON
- adr.md 包含 MADR 标准章节（Context/Decision/Consequences）

**branch:** `phase-4/pr-14-templates`

---

#### PR-15: godoc + CHANGELOG + capability docs

**Wave:** 3
**Scope:** `examples/`（godoc）, `CHANGELOG.md`, `docs/capability-inventory.md`, `docs/master-plan.md`（或等效）
**Tasks:** T62, T63, T64, T65
**Depends:** PR-09 AND PR-10 AND PR-11（三个示例 go build 通过，才能写准确 godoc）

**文件清单（预估）:**
- `examples/todo-order/cells/order-cell/` — 导出类型/函数 godoc 注释
- `examples/sso-bff/` — 导出类型/函数 godoc 注释
- `examples/iot-device/cells/device-cell/` — 导出类型/函数 godoc 注释
- `CHANGELOG.md` — Phase 4 新增条目
- `docs/capability-inventory.md` — Phase 4 能力更新 + v1.0 Scope Cut 附录
- `docs/master-plan.md`（或 roadmap）— v1.0/v1.1 边界更新

**verify 命令:**
```bash
go doc ./examples/todo-order/...
go doc ./examples/sso-bff/...
go doc ./examples/iot-device/...
```

**P1 验收标准:**
- 三个示例的导出类型均有 godoc 注释
- CHANGELOG.md 包含 Phase 4 全部主要交付项
- capability-inventory.md 含 v1.0 Scope Cut 声明

**branch:** `phase-4/pr-15-docs`

---

### Wave 4 PR 组

---

#### PR-16: final validation + gate

**Wave:** 4
**Scope:** 验证脚本 + gate 文档
**Tasks:** T66, T67, T68, T69, T70, T71, T72, T73, T74
**Depends:** PR-09 AND PR-10 AND PR-11 AND PR-12 AND PR-13

**文件清单（预估）:**
- `specs/feat/003-phase4-examples-docs/checklists/validation-report.md` — 验证结果记录
- `specs/feat/003-phase4-examples-docs/checklists/gate-report.md` — 30 分钟 Gate 验证报告
- Phase 4 收尾文档（phase-report + tech-debt 更新 + CHANGELOG 最终版）

**verify 命令（串行执行，每步等待前一步 PASS）:**
```bash
# T66
go build ./...
# T67
go test ./...
# T68
go vet ./...
# T69
gocell validate
# T70
grep -r "cells/.*/internal" examples/  # 期望 0
grep -r "adapters/.*/internal" examples/  # 期望 0
# T71
go test -cover ./kernel/... | awk '/coverage/{if ($NF+0 < 90) {print "FAIL"; exit 1}}'
# T72（需 Docker）
go test -tags=integration -cover ./adapters/postgres/...
# T73（手动）: 按 README 教程从 clone 到 HTTP 200，≤ 30 分钟
```

**P1 验收标准:**
- T66-T70 全部命令零 error 零 warning
- T71: kernel/ 全包覆盖率 ≥ 90%（C-20）
- T72: postgres adapter 覆盖率 ≥ 80%（C-15）
- T73: 30 分钟 Gate 手动验证通过（记录截图或 curl 输出）
- T74: phase-report 写入，tech-debt 更新，CHANGELOG 定稿

**branch:** `phase-4/pr-16-validation-gate`

---

#### PR-17: KG verification

**Wave:** 4（KG 追加，依赖 PR-16 全通过）
**Scope:** `specs/feat/003-phase4-examples-docs/checklists/` + 代码审查
**Tasks:** T75, T76, T77, T78, T79, T80, T81, T82, T83, T84, T85, T86, T87, T88
**Depends:** PR-16

**verify 命令（按约束编号）:**
```bash
# T75 (C-19): kernel/ 零代码修改
git diff develop -- kernel/ | grep "^[+-]" | grep -v "^[+-]\s*//" | wc -l  # 期望 0

# T76 (C-01~C-06): 分层隔离 6 项
grep -r "github.com/ghbvf/gocell/\(runtime\|adapters\|cells\)" kernel/  # 0
grep -r "github.com/ghbvf/gocell/adapters" cells/  # 0
grep -r "github.com/ghbvf/gocell/\(adapters\|cells\)" runtime/  # 0
grep -r "github.com/ghbvf/gocell/cells" adapters/  # 0
grep -r "cells/.*/internal" examples/  # 0
grep -r "adapters/.*/internal" examples/  # 0

# T77 (C-05+C-06): 交叉验证
grep -rn "cells/.*/internal" examples/  # 0
grep -rn "adapters/.*/internal" examples/  # 0

# T78 (C-07+C-08): todo-order cell/slice validate
gocell validate --root examples/todo-order  # 0 error

# T79 (C-13): iot-device L4 无 outboxWriter
grep -n "outboxWriter" examples/iot-device/cells/device-cell/cell.go  # 0

# T80 (C-14 关联): testcontainers build tag
head -1 adapters/postgres/integration_test.go  # "//go:build integration"
head -1 adapters/redis/integration_test.go
head -1 adapters/rabbitmq/integration_test.go

# T81 (C-09): iot-device cell.yaml 必填字段
gocell validate --root examples/iot-device  # 0 error, type=edge, consistencyLevel=L4

# T82 (C-10): contractUsages.role 拓扑合法性
gocell validate  # 含拓扑检查

# T83 (C-11): cell.type 枚举合规
grep "type:" examples/todo-order/cells/order-cell/cell.yaml  # core
grep "type:" examples/iot-device/cells/device-cell/cell.yaml  # edge

# T84 (C-17): HS256 移除
grep -rn "SigningMethodHS256" cells/access-core/  # 0 (或仅 Deprecated path)

# T85 (C-18): sso-bff RSA key pair
grep -n "WithSigningKey\|LoadRSAKeyPair\|NewIssuer" examples/sso-bff/main.go

# T86 (C-23+C-24): kernel 接口签名无变更
git diff develop -- kernel/cell/interfaces.go  # 0 diff
git diff develop -- kernel/outbox/outbox.go  # 0 diff

# T87 (C-25): iot-device contracts 注册
ls contracts/command/device/ contracts/event/device-status/  # 文件存在

# T88 (C-28+C-29): examples/ 编码规范
grep -rn "errors\.New" examples/  # 0 (或仅 test)
grep -rn "fmt\.Println\|log\.Printf" examples/  # 0
```

**P1 验收标准:**
- 上述全部命令返回期望结果（0 匹配/0 diff/PASS）
- KG 验证报告写入 `specs/feat/003-phase4-examples-docs/checklists/kg-verification-report.md`
- 30 条约束（C-01 ~ C-30）全部 PASS

**branch:** `phase-4/pr-17-kg-verification`

---

## Wave 并行示意图

```
Wave 0 (可并行 4 组):
  ├── PR-01 (RS256)         ─→ PR-02 (access-core RS256)─┐
  ├── PR-03 (outbox failfast) ──────────────────────────→ PR-09 / PR-10 / PR-11 (Wave 2 入口 gate)
  ├── PR-04 (S3 + infra)    [独立，随时可合并]
  └── PR-05 (testcontainers) ─→ PR-06 / PR-07 (Wave 1 并行)─→ PR-08

Wave 1 (PR-06 和 PR-07 并行，等 PR-05):
  ├── PR-06 (postgres integration)
  └── PR-07 (redis + rabbitmq integration)
      └──→ PR-08 (outbox fullchain，等 PR-06 + PR-07)

Wave 2 (三流水线并行，等 PR-02 AND PR-03):
  ├── PR-09 (todo-order)    [最长，9 跳，关键路径]
  ├── PR-10 (sso-bff)       [4 跳]
  └── PR-11 (iot-device)    [7 跳]

Wave 3 (部分并行):
  ├── PR-12 (CI)            [等 PR-05 + PR-08]
  ├── PR-13 (README)        [等 PR-09，串行 4 节]  [关键路径]
  ├── PR-14 (templates)     [无依赖，最早可完成]
  └── PR-15 (docs)          [等 PR-09 + PR-10 + PR-11]

Wave 4 (串行门控):
  PR-16 (validation gate)   [等 PR-09+PR-10+PR-11+PR-12+PR-13]
  └──→ PR-17 (KG verification)  [等 PR-16]
```

---

## 文件冲突风险分析

| 文件 | 涉及 PR | 冲突风险 | 处理建议 |
|------|---------|---------|---------|
| `cells/access-core/cell.go` | PR-02 (T04), PR-03 (T10) | 高 | PR-03 基于 PR-02 分支；或合并 T04+T10 到同一 PR |
| `go.mod` / `go.sum` | PR-05 (T18) | 低 | PR-05 最早合并，其他 PR rebase |
| `docker-compose.yml` (根目录) | PR-04 (T16) | 低 | 单 PR 独立修改，无冲突 |
| `contracts/` 目录 | PR-09 (T33), PR-11 (T43/T45) | 低 | 不同子目录，无冲突 |

**关键冲突处理原则:** PR-02 和 PR-03 修改同一文件 `cells/access-core/cell.go`。推荐方案：将 T04（access-core WithJWTIssuer）和 T10（access-core outboxWriter fail-fast）合并到同一 PR（PR-02 扩展或新建 PR-02+）。这样 PR-03 只处理 T09 + audit-core/config-core (T11/T12/T13)，规避文件冲突。

---

## PR 统计

| Wave | PR 数 | 任务数 | 可并行 PR | 关键路径 PR |
|------|-------|-------|---------|-----------|
| Wave 0 | 5 (PR-01~05) | 18 | PR-01/PR-03/PR-04/PR-05 | PR-01→PR-02 |
| Wave 1 | 3 (PR-06~08) | 8 | PR-06‖PR-07 | PR-06→PR-08 |
| Wave 2 | 3 (PR-09~11) | 23 | PR-09‖PR-10‖PR-11 | PR-09 |
| Wave 3 | 4 (PR-12~15) | 16 | PR-13‖PR-14 | PR-13 |
| Wave 4 | 2 (PR-16~17) | 23 | 串行 | PR-16→PR-17 |
| **总计** | **17** | **88** | - | **PR-01→02→09→13→16** |
