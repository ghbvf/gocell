# Kernel Constraints Report -- Phase 4: Examples + Documentation

## 审查人: Kernel Guardian
## 日期: 2026-04-06
## 分支: feat/003-phase4-examples-docs
## 基线 commit: 28ac80f (Phase 3 complete, capability inventory)

---

## (a) 内核集成修改建议 (KG-01 ~ KG-10)

### KG-01: access-core HS256 -> RS256 迁移必须修改 cell.go 的 Init 签名流，不得触碰 kernel/cell 接口

**问题**: spec FR-7.1/FR-7.2 要求 RS256 默认化。当前 `cells/access-core/cell.go:140-150` 中 `Init` 方法通过 `c.signingKey []byte` 做 HS256 密钥校验。迁移到 RS256 后需改为 `*rsa.PrivateKey` / `*rsa.PublicKey` 注入。
**约束**: `kernel/cell/interfaces.go` 的 `Cell.Init(ctx, Dependencies)` 签名不得修改。Dependencies struct 当前使用 `Config map[string]any`，可通过此 map 传递 RSA key pair（或通过 Cell-level Option 注入），但不得在 kernel/cell 中引入 `crypto/rsa` 依赖。
**建议**: (1) 在 `cells/access-core/cell.go` 新增 `WithRSAKeyPair(priv *rsa.PrivateKey, pub *rsa.PublicKey) Option`；(2) Init 中优先使用 RSA key pair，无 RSA key pair 时 fail-fast（返回 `ERR_AUTH_MISSING_KEY`），删除 HS256 fallback；(3) 保留 `WithSigningKey([]byte)` 但标记 `// Deprecated`，仅用于测试。(4) sessionlogin/sessionrefresh/sessionvalidate 三个 slice 的构造函数从接受 `signingKey []byte` 改为接受 `auth.JWTIssuer`/`auth.JWTVerifier` 接口。
**影响层**: cells/ + runtime/auth（kernel/ 零修改）

### KG-02: outboxWriter fail-fast 必须在 Cell.Init 阶段校验，不能在运行时静默降级

**问题**: spec FR-7.3 要求 L2+ Cell 在 Init 阶段校验 outboxWriter != nil。当前 `cells/access-core/cell.go:156-180` 中 outboxWriter 是可选注入（`if c.outboxWriter != nil`），缺失时 slice service 层的 publish 逻辑会 fallback 到 `publisher.Publish`（非事务性），打破 L2 一致性承诺。
**约束**: 校验逻辑必须放在 `Cell.Init()` 中（在 `BaseCell.Init(ctx, deps)` 之后），不得修改 `kernel/cell/base.go` 的 `BaseCell.Init` 方法。kernel 层不感知 outboxWriter 的存在——这是 cells/ 和 runtime/ 的职责。
**建议**: (1) `access-core/cell.go:Init` 在 `BaseCell.Init` 调用后增加 `if c.meta.ConsistencyLevel >= cell.L2 && c.outboxWriter == nil { return errcode.New("ERR_CELL_MISSING_OUTBOX", ...) }`；(2) audit-core 和 config-core 同理；(3) 同时在 3 个 Cell 的 outboxWriter fallback 处添加 `slog.Warn` 作为过渡可观测手段（即使 Init 校验已阻止，防御性编程）。
**影响层**: cells/（kernel/ 零修改）

### KG-03: examples/ 分层依赖合规必须有自动化检查，不能仅靠 go build 隐式验证

**问题**: spec FR-10.4 和 NFR-1 声明"零分层违反"，但验证方式仅为"go build + grep"。examples/ 内部可能 import `cells/access-core/internal/*` 或跨 Cell internal 包，这些在 Go 编译时不报错（同一 module 内 internal 包只限同一目录树）。
**约束**: GoCell 的 `internal/` 目录语义是 Cell 内部封装边界，Go 编译器对同一 module 内的 internal 包限制比预期宽松——`examples/sso-bff/` 可以合法 import `cells/access-core/internal/domain`，这在分层语义上是违规的。
**建议**: (1) 在 CI workflow（FR-8.1）中加入 `grep -r "cells/.*/internal" examples/` 检查，非零匹配即 FAIL；(2) 同时检查 `grep -r "adapters/.*/internal" examples/`；(3) 将这些检查编码到 `gocell validate` 中（但这是 kernel/ 工具层变更，可在 Phase 4 后执行；CI grep 为 Phase 4 最小方案）。
**影响层**: CI workflow + 可选 kernel/governance

### KG-04: testcontainers 集成测试必须使用 build tag 隔离，不得影响默认 go test 行为

**问题**: spec FR-10.2 要求 `//go:build integration` 标签。当前 adapter integration_test.go 文件无 build tag（均为 `t.Skip` stub）。升级为 testcontainers 后，若忘记加 build tag，`go test ./adapters/...` 在无 Docker 环境下会直接 FAIL 而非 SKIP。
**约束**: kernel/ 测试不受影响（kernel 不依赖 adapters），但如果 `go test ./...`（全仓库）因 adapter 集成测试失败而中断，kernel 覆盖率报告可能受干扰。
**建议**: (1) 每个 integration_test.go 第一行必须为 `//go:build integration`；(2) CI workflow 中 `go test ./...` 不带 -tags=integration，专门的 integration job 带 `-tags=integration`；(3) 在 Phase 4 S5（实施）阶段的代码审查清单中添加此检查项。
**影响层**: adapters/（kernel/ 零影响）

### KG-05: sso-bff 示例中 access-core 的 outboxWriter 注入必须使用真实 postgres adapter，不能使用 nil

**问题**: spec FR-1.3 要求演示 `event.session.created` 发布（outbox）。sso-bff 使用 access-core（L2），按 KG-02 的 fail-fast 规则，Init 阶段必须注入 outboxWriter。示例如果使用 in-memory fallback 模式运行，会绕过 outboxWriter 校验（因为 in-memory 模式通常跳过该校验）。
**约束**: 示例代码必须展示完整的生产模式配线（PostgreSQL adapter outboxWriter 注入），而非测试模式。这是示例的教育价值核心——评估者需要看到 L2 一致性的真实实现路径。
**建议**: (1) sso-bff main.go 必须展示 `postgres.NewOutboxWriter(pool)` 注入到 access-core/audit-core/config-core；(2) docker-compose 降级模式（无 Docker）可提供 in-memory fallback，但 README 必须明确标注"降级模式不保证 L2 一致性"；(3) 默认启动路径必须是 docker compose up -d + 真实 adapter。
**影响层**: examples/（kernel/ 零影响，但验证了 kernel 设计的正确性）

### KG-06: todo-order 示例的 cell.yaml 和 slice.yaml 必须通过 gocell validate 验证

**问题**: spec FR-2.1/FR-2.2 要求 order-cell 包含 `cell.yaml`。todo-order 是"从零创建业务 Cell"的 golden path，其元数据文件必须成为合规范本。
**约束**: cell.yaml 必须含 id/type/consistencyLevel/owner{team,role}/schema.primary/verify.smoke（全部 6 个必填字段）。slice.yaml 必须含 id/belongsToCell/contractUsages/verify.unit/verify.contract。`gocell validate` 必须对 examples/ 下的元数据文件零 error。
**建议**: (1) 在 S5 实施 order-cell 后立即运行 `gocell validate`；(2) S7 验证阶段的 evidence/validate/result.txt 必须包含 examples/ 下的元数据验证结果；(3) todo-order 的 contract YAML（http.order.v1, event.order.created.v1）必须在 contracts/ 目录下注册，而非示例内部私有定义。
**影响层**: examples/ + contracts/（kernel/governance 验证逻辑无修改，但验证范围扩大）

### KG-07: iot-device 示例的 L4 Cell 不得声明 outboxWriter 依赖

**问题**: spec FR-3.1 定义 device-cell 为 L4 (DeviceLatent)。L4 的一致性模式是"设备长延迟闭环"，不使用 outbox pattern（那是 L2 的模式）。L4 使用命令入队 + 回执确认模式。
**约束**: 如果 device-cell Init 中注入了 outboxWriter，会给评估者传达错误的一致性模型认知——L4 不等于 L2+延迟，而是一种完全不同的一致性策略。
**建议**: (1) device-cell 使用 `outbox.Publisher.Publish` 直接发布事件（非事务性），或使用命令队列模式；(2) cell.yaml 中 consistencyLevel 声明为 L4，Init 中不注入 outboxWriter；(3) README 中解释 L4 与 L2 的一致性差异。
**影响层**: examples/（kernel/ 零影响）

### KG-08: CI workflow 的 gocell validate 步骤必须覆盖全仓库（含 examples/）

**问题**: spec FR-8.1 的 CI workflow 包含 `gocell validate`。但 validate 当前的扫描范围取决于 `kernel/governance/targets.go` 的路径发现逻辑。如果 examples/ 下的 cell.yaml/slice.yaml/contract.yaml 路径不在 targets 扫描范围内，CI 中的 validate 不会覆盖示例的元数据文件。
**约束**: kernel/governance 的扫描路径是相对于项目根目录的 `cells/`、`contracts/`、`journeys/`、`assemblies/`。examples/ 下的元数据文件路径为 `examples/todo-order/cells/order-cell/cell.yaml`，不在默认扫描路径中。
**建议**: (1) 评估 kernel/governance/targets.go 是否需要新增 `examples/*/cells/**` 扫描路径（这会修改 kernel/ 代码）；(2) 或者在 CI workflow 中对 examples/ 单独运行 `gocell validate --root examples/todo-order`（不修改 kernel/）；(3) 推荐方案 (2)，保持 kernel/ 零修改。
**影响层**: CI workflow 配置（kernel/ 零修改的方案优先）

### KG-09: S3 环境变量前缀修复必须同步更新 .env.example

**问题**: spec FR-7.4 要求 `adapters/s3/client.go:ConfigFromEnv()` 从 `S3_*` 改为 `GOCELL_S3_*`。当前 `client.go:41-48` 使用 `os.Getenv("S3_ENDPOINT")` 等无前缀变量名。
**约束**: 环境变量前缀变更是破坏性变更——依赖旧变量名的部署环境会静默获取空值。虽然当前无生产部署，但 docker-compose.yml 和 .env.example 中如果使用旧名称会导致示例无法运行。
**建议**: (1) 修改 ConfigFromEnv 后，同时更新 .env.example；(2) docker-compose.yml 中 minio 服务的环境变量不受影响（那是容器内部变量），但应用侧 .env 必须使用 GOCELL_S3_* 前缀；(3) client_test.go 中 TestConfigFromEnv 的 t.Setenv 调用必须同步更新。
**影响层**: adapters/s3/ + 配置文件（kernel/ 零影响）

### KG-10: kernel/ 覆盖率维持门控必须在 CI 中自动化，不能依赖手动检查

**问题**: spec FR-10.3 和 SC-5 要求 kernel/ 覆盖率维持 >= 90%。Phase 3 结果为 93.2%-100%。Phase 4 不应修改 kernel/ 代码，但如果意外修改（例如修复 KG-08 方案(1) 中的 governance/targets.go），覆盖率可能下降。
**约束**: kernel/ 覆盖率 >= 90% 是硬性 Gate 要求。CI workflow 必须在每次 PR 中自动检测。
**建议**: (1) CI workflow 中添加 `go test -cover ./kernel/... | grep -E 'coverage: ([0-9]+\.[0-9]+)%' | awk '{if ($NF+0 < 90) exit 1}'`（或等效脚本）；(2) 作为 CI 失败条件而非仅信息输出；(3) 同理 adapters/postgres 的 >= 80% 门控也应自动化。
**影响层**: CI workflow（kernel/ 零修改）

---

## (b) 集成风险评估

### 整体风险级别: 中

**理由**:

Phase 4 的核心交付是 examples/ 和文档，这些不直接修改 kernel/ 代码。但 Phase 3 遗留的 tech-debt 修复（RS256 默认化 FR-7.1/7.2、outboxWriter fail-fast FR-7.3）涉及 cells/ 和 runtime/ 层的签名变更。以下是逐项风险评估：

| 风险项 | 级别 | 说明 |
|--------|------|------|
| R-01: RS256 迁移引入编译破坏 | 高 | access-core 3 个 slice（sessionlogin, sessionrefresh, sessionvalidate）需从 `[]byte signingKey` 改为 `auth.JWTIssuer/JWTVerifier` 接口注入。当前 11 处 HS256 引用需全部替换。如果迁移不完整，sso-bff 示例会编译失败或运行时 panic |
| R-02: outboxWriter fail-fast 打破现有测试 | 中 | 当前 access-core/audit-core/config-core 的测试中，outboxWriter 通常为 nil（测试不注入）。添加 Init fail-fast 后，所有未注入 outboxWriter 的测试用例会 Init 失败。需要为测试场景引入 `WithOutboxWriter(noopWriter)` 或 `WithTestMode()` 选项 |
| R-03: testcontainers 引入增加 go.mod 依赖树 | 低 | testcontainers-go 的依赖链较深（Docker SDK + 多个间接依赖），可能与现有依赖冲突。但由于仅在 integration build tag 下使用，不影响主构建 |
| R-04: 3 个示例项目的 main.go 接线复杂度 | 中 | sso-bff 需要组装 3 个 Cell + 6 个 adapter（PostgreSQL pool、Redis client、RabbitMQ publisher/subscriber、outboxWriter、outboxRelay）。接线代码是当前框架中不存在的新代码，容易出错 |
| R-05: docker-compose 启动时序影响示例可靠性 | 低 | rabbitmq/minio 缺 start_period 可能导致首次启动时连接失败。FR-8.2 已覆盖，但需在所有 3 个示例的 docker-compose 中一致配置 |
| R-06: kernel/ 意外修改 | 低 | 如果 KG-08 选方案(1)修改 governance/targets.go，可能影响覆盖率。推荐方案(2)避免此风险 |

**关键缓解措施**:
1. RS256 迁移先在 runtime/auth 层验证（该层已就绪），再逐个 Cell 切换，每切换一个 Cell 运行全量 go test
2. outboxWriter fail-fast 必须同步引入 noop writer（用于测试），否则现有 60+ 个测试会批量失败
3. 3 个示例按复杂度递增实施：todo-order（最简单的自定义 Cell）-> sso-bff（组合内建 Cell）-> iot-device（L4 高级模式）

---

## (c) 本 Phase 必须验证的内核约束清单

以下约束清单覆盖 Phase 4 scope 内所有需验证的 GoCell 核心约束。每条约束附验证方法和验收标准。

### 分层隔离约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-01 | kernel/ 不 import runtime/adapters/cells/ | `grep -r "github.com/ghbvf/gocell/(runtime\|adapters\|cells)" src/kernel/` | 0 匹配 |
| C-02 | cells/ 不 import adapters/ | `grep -r "github.com/ghbvf/gocell/adapters" src/cells/` | 0 匹配 |
| C-03 | runtime/ 不 import adapters/cells/ | `grep -r "github.com/ghbvf/gocell/(adapters\|cells)" src/runtime/` | 0 匹配 |
| C-04 | adapters/ 不 import cells/ | `grep -r "github.com/ghbvf/gocell/cells" src/adapters/` | 0 匹配 |
| C-05 | examples/ 不 import cells/*/internal/ | `grep -r "cells/.*/internal" examples/` 或 `src/examples/` | 0 匹配 |
| C-06 | examples/ 不 import adapters/*/internal/ | `grep -r "adapters/.*/internal" examples/` | 0 匹配 |

### 元数据合规约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-07 | todo-order cell.yaml 含全部必填字段 | `gocell validate` 覆盖 examples/ | 零 error |
| C-08 | todo-order slice.yaml 含全部必填字段 | 同上 | 零 error |
| C-09 | iot-device cell.yaml 含全部必填字段 | 同上 | 零 error |
| C-10 | contractUsages.role 匹配 kind 合法角色 | `gocell validate` 拓扑检查 | http->serve/call, event->publish/subscribe |
| C-11 | cell.type in {core, edge, support} | 检查 order-cell=core, device-cell=edge | 符合声明 |

### 一致性等级约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-12 | L2 Cell（access/audit/config）Init 阶段 outboxWriter != nil | 代码审查 + 单元测试 | Init 缺 outboxWriter 时返回 ERR_CELL_MISSING_OUTBOX |
| C-13 | L4 Cell（device-cell）不依赖 outboxWriter | 代码审查 | Init 不校验 outboxWriter，不 fallback 到 outbox pattern |
| C-14 | outbox 全链路（write->relay->publish->consume->idempotency）testcontainers PASS | `go test ./adapters/... -tags=integration` | TestIntegration_OutboxFullChain PASS |
| C-15 | postgres adapter 覆盖率 >= 80% | `go test -cover ./adapters/postgres/...` | >= 80% |

### 安全约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-16 | RS256 默认签发，无 RSA key pair 时 fail-fast | 代码审查 + 单元测试 | `NewJWTIssuer(nil, ...)` 返回错误而非降级 HS256 |
| C-17 | access-core 3 slice 不含 HS256 默认路径 | `grep -r "SigningMethodHS256" src/cells/access-core/` | 0 匹配（或仅在 test 文件/Deprecated path 中出现） |
| C-18 | sso-bff 示例使用 RSA key pair | 代码审查 | main.go 中使用 auth.LoadRSAKeyPair 或等效 |

### Kernel 稳定性约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-19 | kernel/ 代码零修改（或仅文档注释） | `git diff develop -- src/kernel/ \| grep "^[+-]" \| grep -v "^[+-]\s*//"` | 零行非注释修改 |
| C-20 | kernel/ 覆盖率全包 >= 90% | `go test -cover ./kernel/...` | assembly>=95%, cell>=99%, governance>=96%, metadata>=97%, registry>=100%, scaffold>=93%, slice>=94% |
| C-21 | kernel/ go vet 零警告 | `go vet ./kernel/...` | 零输出 |
| C-22 | gocell validate 零 error | `gocell validate` | 0 error(s) |

### 契约与接口约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-23 | kernel/cell.Cell 接口签名无变更 | `git diff develop -- src/kernel/cell/interfaces.go` | 零 diff（或仅注释） |
| C-24 | kernel/outbox.Writer/Publisher/Subscriber 接口签名无变更 | `git diff develop -- src/kernel/outbox/outbox.go` | 零 diff（或仅注释） |
| C-25 | todo-order 和 iot-device 的 contract YAML 注册到 contracts/ | 文件存在性检查 | contracts/ 下有对应 contract 定义 |

### 适配器接口约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-26 | adapters/ 实现 kernel/ 或 runtime/ 定义的接口 | 编译时断言 `var _ Interface = (*Impl)(nil)` | go build 通过 |
| C-27 | S3 ConfigFromEnv 使用 GOCELL_S3_* 前缀 | 代码审查 + 单元测试 | `os.Getenv("GOCELL_S3_ENDPOINT")` 等 |

### 编码规范约束

| 编号 | 约束 | 验证方法 | 验收标准 |
|------|------|---------|---------|
| C-28 | examples/ 使用 pkg/errcode 而非裸 errors.New | `grep -rn "errors.New" examples/` | 零匹配（或仅在 test 文件中） |
| C-29 | examples/ 使用 slog 而非 fmt.Println/log.Printf | `grep -rn "fmt.Println\|log.Printf" examples/` | 零匹配 |
| C-30 | WithEventBus 标注 Deprecated | 代码审查 | `// Deprecated: Use WithPublisher and WithSubscriber instead.` |

---

## (d) 工作流可执行性评估

### 总体评估: 可执行，但有 2 个卡点风险

Phase 4 spec 定义了 10 个 FR（含 ~50 子项），覆盖 examples 3 个 + README + templates 6 个 + testcontainers + 安全加固 + CI + 文档。工作量约 4000-6000 行新增代码。

### 9 阶段（S0-S8）逐阶段评估

| 阶段 | 可执行性 | 潜在卡点 |
|------|---------|---------|
| S0 Kickoff | 顺畅 | phase-charter.md 已就位，role-roster 需更新（前端开发者 OFF 第 4 Phase 需正式声明永久 N/A 或在框架生命周期内关闭） |
| S1 Spec Review | 顺畅 | spec.md 结构完整，10 FR 边界清晰 |
| S2 Cross-Review | 顺畅 | Architect/PM/KG/Roadmap 四份审查报告。KG 约束清单（本文件）已就绪 |
| S3 Decisions | 顺畅 | 关键裁决项: (a) examples/ 是否使用独立 go.mod（spec 假设使用根 module）；(b) RS256 迁移的 test noop writer 设计 |
| S4 Planning | 需注意 | 任务拆解需处理 FR 间的隐式依赖。见下文"关键依赖链" |
| S5 Implementation | 卡点风险 1 | RS256 迁移 + outboxWriter fail-fast 修改 cells/ 层，可能导致现有 60+ 单元测试批量失败。必须先引入 noop writer / RSA test key pair 工具，再修改 Cell Init 逻辑 |
| S6 Review | 顺畅 | 审查维度明确（分层 + 元数据 + 覆盖率 + 安全） |
| S7 Validation | 卡点风险 2 | testcontainers 集成测试依赖 Docker。如果 CI 环境不支持 DinD（Docker-in-Docker），集成测试在 CI 中无法运行。spec Edge Cases 已提及此风险但未给出具体解决方案。本地验证可通过，但 CI 验证可能受阻 |
| S8 Gate | 顺畅 | Gate 标准明确（30 分钟首个 Cell），可手动验证 |

### 关键依赖链（S4 Planning 必须处理）

```
Wave 0 (基础设施):
  TD-fix: RS256 test key pair 工具 + noop outboxWriter
  TD-fix: S3 env prefix
  TD-fix: docker-compose start_period
  TD-fix: WithEventBus Deprecated
  CI workflow

Wave 1 (安全+一致性修复，依赖 Wave 0):
  FR-7.1: RS256 默认化 (runtime/auth) -- 已就绪，仅需确认
  FR-7.2: access-core RS256 切换 -- 依赖 Wave 0 test key pair
  FR-7.3: outboxWriter fail-fast -- 依赖 Wave 0 noop writer

Wave 2 (testcontainers，依赖 Wave 1):
  FR-6.1-6.6: testcontainers 集成测试 -- 依赖 Wave 1 的 outboxWriter 正确性

Wave 3 (examples，依赖 Wave 1+2):
  FR-2: todo-order (最简单，先做)
  FR-1: sso-bff (依赖 Wave 1 的 RS256 切换)
  FR-3: iot-device (依赖 Wave 3-todo 的模式验证)

Wave 4 (文档+模板，可部分并行):
  FR-4: README Getting Started (依赖至少 todo-order 可运行)
  FR-5: templates/ (无代码依赖，可并行)
  FR-9: CHANGELOG + capability-inventory + godoc
```

### 卡点 1 详细分析: RS256 迁移的测试级联失败

**当前状态**: `cells/access-core/slices/sessionlogin/service.go:230-236` 默认使用 HS256。`cells/access-core/cell.go:140-150` 通过 `[]byte signingKey` 做密钥校验。

**迁移后**: signingKey 改为 RSA key pair，现有所有使用 `WithSigningKey([]byte("test-secret-32-bytes-minimum-len"))` 的测试都会失败。

**缓解方案**: 
1. 在 `runtime/auth/keys.go` 或新文件中提供 `MustGenerateTestKeyPair() (*rsa.PrivateKey, *rsa.PublicKey)` 工具函数（生成 2048-bit 测试用 RSA key pair）
2. 所有 Cell 测试文件的 `TestMain` 或 setup 中使用此工具函数替换硬编码 `[]byte` key
3. 迁移顺序: keys.go 工具 -> sessionvalidate -> sessionlogin -> sessionrefresh -> access-core cell.go -> cell_test.go

### 卡点 2 详细分析: testcontainers CI 环境

**当前状态**: `.github/workflows/ci.yml` 不存在。testcontainers-go 不在 go.mod。

**CI 环境约束**: GitHub Actions 默认 runner 支持 Docker（ubuntu-latest 自带 Docker daemon），因此 testcontainers 在 GHA 中可直接运行，无需 DinD。

**缓解方案**: 
1. CI workflow 分为两个 job: `test`（默认 go test，快速反馈）和 `integration`（-tags=integration，需 Docker）
2. `integration` job 的 `services` 字段不需要——testcontainers 自行管理容器
3. 如果 CI 环境受限（如 macOS runner 无 Docker），integration job 添加 `runs-on: ubuntu-latest` 硬约束

---

## 附录: Phase 3 -> Phase 4 约束继承追踪

| Phase 3 约束编号 | Phase 3 状态 | Phase 4 处理 |
|-----------------|-------------|-------------|
| C-13 (shutdown 顺序验证) | 未验证 | 通过 testcontainers 集成测试验证 -> C-14 |
| C-19 (adapter 错误使用 errcode) | PARTIAL | 延续到 Phase 4，examples/ 必须合规 -> C-28 |
| C-21 (L2 操作使用 outbox.Writer in tx) | PASS (代码层面) | 端到端验证 -> C-14 (testcontainers) |
| Must-Fix K1 (集成测试) | DEFERRED | -> FR-6, C-14/C-15 |
| Must-Fix K2 (RS256) | PARTIAL | -> FR-7.1/7.2, C-16/C-17/C-18 |
| Must-Fix K3 (outboxWriter fail-fast) | DEFERRED | -> FR-7.3, C-12 |

---

## 审查结论

Phase 4 spec 从内核集成视角是合理的。核心设计原则——kernel/ 零修改、变更集中在 cells/runtime/adapters/examples/ 层——与 GoCell 分层架构一致。

**必须修复项（不超过 3 条）**:

1. **RS256 迁移必须提供 test key pair 工具，否则 S5 实施阶段会因测试级联失败而卡住**（KG-01 + 卡点 1）。迁移前先在 Wave 0 交付 `runtime/auth.MustGenerateTestKeyPair()`，再逐个 Cell 切换。

2. **outboxWriter fail-fast 必须同步引入 noop writer for test，否则现有 Cell 测试全部 Init 失败**（KG-02 + R-02）。建议在 `kernel/outbox` 包新增 `NoopWriter` 实现（因为是接口的测试辅助，放在接口定义包是惯例），或在 `pkg/testutil` 中提供。

3. **examples/ 的 internal/ import 检查必须纳入 CI，go build 不足以捕获分层语义违规**（KG-03）。最小方案: CI workflow 中 3 行 grep 命令。

---

*Generated by Kernel Guardian on 2026-04-06*
*Baseline: commit 28ac80f (Phase 3 complete)*
*Scope: Phase 4 spec.md + product-context.md + phase-charter.md*
