# Archtest 不变式编目 B — Outbox / EventBus / RMQ / Saga / Relay / Subscription

本文件覆盖主题：事件驱动与最终一致性约束（outbox 生命周期、RabbitMQ 频道/发布/停机、Saga 执行纪律、Relay 生命周期隔离、订阅身份语义、SafeID 封装、健康聚合、事件 DTO 命名规范）。

来源文件（均位于 `tools/archtest/`）：
`outbox_invariants_test.go`, `outboxtest_import_boundary_test.go`, `rmq_invariants_test.go`,
`saga_journal_conformance_enrollment_test.go`, `saga_journal_holder_seal_test.go`,
`saga_leader_gate_test.go`, `saga_step_run_outside_tx_test.go`, `relay_isolation_test.go`,
`subscription_invariants_test.go`, `event_subscription_contractgen_coverage_test.go`,
`safeid_funnel_test.go`, `aftercommit_pure_transient_test.go`,
`visit_buffer_then_commit_test.go`, `l2_outbox_atomicity_coverage_test.go`,
`health_aggregation_test.go`, `event_camelcase_invariants_test.go`

**本文件 35 条 | Hard 18 / Medium 15 / Soft 2 | 通用 2 / 半通用 4 / 专属 29**

> 计数说明：funnel 双向评级（如 `Hard↓/Medium↑`）按整体较低档计入 Medium；Soft 条均为子规则内遗留形态（B1 盲区逆向自检），不新引入。

---

### 批次 1 — Outbox 核心（OUTBOX-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| OUTBOX-LEASE-ID-CAS-01 | Medium（推断） | AST-pattern | 五条 outbox SQL 常量必须以 lease_id 做 CAS 防重，防止陈旧 worker 覆写新 owner 行 | adapters/postgres | 专属 |
| OUTBOX-MARK-RETURNS-BOOL-01 | Medium（推断） | AST-pattern | relay*.go 调用 MarkPublished/MarkRetry/MarkDead 必须绑定 bool 返回，禁止 `_` 丢弃，防止 CAS 未命中被误计成功 | runtime/outbox, adapters/postgres | 专属 |
| OUTBOX-METADATA-MAX-BYTES-01 | Medium（推断） | AST-pattern | outbox_writer.go 的 Write 和 encodeBatchEntry 方法必须引用 MaxMetadataBytes 常量，确保 metadata 写入有上限 | adapters/postgres, kernel/outbox | 专属 |
| OUTBOX-PAYLOAD-SIZE-01 | Medium（推断） | AST-pattern | kernel/outbox.Entry.Validate 必须用 MaxPayloadBytes 做 `len(Payload) > MaxPayloadBytes` 比较，单纯声明常量不够 | kernel/outbox | 专属 |
| OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01 | Medium（推断） | reflect-field-freeze | HandleResult 结构体禁止声明 Receipt 字段，阻止 cell handler 写已废弃的 Settlement 路径 | kernel/outbox | 专属 |
| OUTBOX-RELAY-LOST-METRIC-01 | Medium（推断） | AST-pattern | 含 A/B 子条：handleFailedEntry 必须绑定 Mark{Retry,Dead} 的 bool 返回；PollCycleResult 必须声明 Lost 字段，使 stale-lease 路径可观测 | runtime/outbox, kernel/outbox | 专属 |
| OUTBOX-SERVICE-01 | Medium（推断） | AST-pattern | slice service.go 禁止对 txRunner == nil 做 nil 分支（SERVICE-01..05 联合测试） | cells/**/slices | 专属 |
| OUTBOX-SERVICE-02 | Medium（推断） | AST-pattern | slice service.go 禁止直接调用 Publisher.Publish，必须走 outbox 事务路径 | cells/**/slices, kernel/outbox | 专属 |
| OUTBOX-SERVICE-03 | Medium（推断） | import-ban | slice service.go 禁止 import runtime/outbox，保持 cell 层不依赖 runtime 层 | cells/**/slices, runtime/outbox | 专属 |
| OUTBOX-SERVICE-04 | Medium（推断） | AST-pattern | slice service.go 禁止依赖 outbox.Publisher 或构造 DirectEmitter，mode 解析属于 Cell 边界 | cells/**/slices, kernel/outbox | 专属 |
| OUTBOX-SERVICE-05 | Medium（推断） | AST-pattern | slice service.go 禁止定义 WithOutboxWriter option，服务层只允许 WithEmitter/WithTxManager | cells/**/slices, kernel/outbox | 专属 |
| OUTBOX-TOPIC-FAILOPEN-01 | Medium（推断） | AST-pattern | 安全敏感 topic（session.*、user.*、role.*、audit.*、event.* 安全合约）禁止设置 FailurePolicyFailOpen，via go/types 常量折叠 | kernel/outbox | 专属 |

---

### 批次 2 — Outbox 结构冻结与工厂优先（OUTBOX-HANDLERESULT-*、OUTBOXTEST-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| OUTBOX-HANDLERESULT-FIELDS-FROZEN-01 | Medium（推断） | reflect-field-freeze | kernel/outbox.HandleResult 字段集冻结为 4 个（Disposition/Err/ProcessReason/SettlementObservers），新增须过 allowlist | kernel/outbox | 专属 |
| OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01 | Medium（推断） | callsite-allowlist | 生产代码必须用 Ack/Requeue/Reject 工厂，禁止 HandleResult{...} 字面量构造（allowlist 限内核 plumbing），via go/types 类型感知扫描 | kernel/outbox | 专属 |
| METADATA-LIMITS-SINGLE-SOURCE-01 | Medium（推断） | AST-pattern | MaxMetadataKeys/KeyLen/ValueLen/TotalSize 仅允许在 kernel/metautil 声明一次，禁止其他包重复定义 | kernel/metautil | 半通用 |
| OUTBOXTEST-CLOSE-VIA-BUDGET-01 | Medium | callsite-allowlist | outboxtest 包内所有 Subscriber.Close 调用必须位于 closeWithBudget 函数内，防止裸 Close 泄漏 goroutine；via ResolveMethodCall 类型感知 | kernel/outbox/outboxtest | 专属 |
| OUTBOXTEST-IMPORT-BOUNDARY-01 | Hard（推断） | import-ban | 生产 Go 文件禁止 import kernel/outbox/outboxtest，防止通过 Recorder.CellEmitter() 绕过 sealed emitter funnel | kernel/outbox/outboxtest | 专属 |

---

### 批次 3 — RabbitMQ 频道与连接（RMQ-CHANNEL-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01 | Hard（推断） | type-system-seal | adapters/rabbitmq 内 AMQPChannel.Close() 必须通过 Connection.CloseEphemeralChannel，防止绕过 inUseChannels 计数；via go/types.Implements 类型感知，命名免疫 | adapters/rabbitmq | 专属 |
| RMQ-CHANNEL-MAX-PER-CONN-01 | Medium（推断） | AST-pattern | 含 A/B/C 子条：Config.MaxChannelsPerConn 字段必须存在；setDefaults 必须用 `<= 0` 条件赋默认值常量；AcquireChannel 必须引用 inUseChannels 计数器 | adapters/rabbitmq | 专属 |

---

### 批次 4 — RabbitMQ 发布与停机（RMQ-PUBLISHER-*、RMQ-STOPINTAKE-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| RMQ-PUBLISHER-FAILURE-HANDLING-01 | Medium（推断） | AST-pattern | 含 A/B/C/D 子条：Publish 必须引用 ErrAdapterAMQPNack；至少 3 次 slog.Warn；调用 RecordPublishFailure；每个非成功 return 分支必须含 RecordPublishFailure | adapters/rabbitmq | 专属 |
| RMQ-PUBLISHER-RELEASES-CHANNEL-01 | Medium（推断） | AST-pattern | Publisher.Publish 必须将 AcquireChannel 与 defer CloseEphemeralChannel/ReleaseChannel 配对，防止 inUseChannels 泄漏 | adapters/rabbitmq | 专属 |
| RMQ-STOPINTAKE-INFLIGHT-WAIT-01 | Medium（推断） | AST-pattern | 含 A/B 子条：StopIntake 必须等待 in-flight 投递（inflightCount poll）；drainRemaining 禁止 `case <-ctx.Done()` 且必须使用 context.WithoutCancel | adapters/rabbitmq | 专属 |

---

### 批次 5 — Saga 执行纪律（SAGA-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 | Medium | conformance-test | 每个实现 SagaJournal 接口的类型必须出现在合规测试调用点，防止新实现逃过测试覆盖 | kernel/saga | 专属 |
| SAGA-JOURNAL-HOLDER-SEAL-01 | Hard↓/Medium↑ | single sanctioned holder | 持有 kernel/saga.Journal 字段的 struct 仅限 Coordinator，via go/types 类型感知扫描；upstream 仅 Medium（无包私有 seal） | kernel/saga | 专属 |
| SAGA-DRIVE-BEHIND-LEADER-GATE-01 | Medium | AST-pattern | 含 A1/A2/A3 子条：driveOne 只能在 tickOnce 内调用；tickOnce 必须调用 AcquireLead；lead 返回值必须控制 driveOne 是否执行 | runtime/saga | 专属 |
| SAGA-STEP-RUN-OUTSIDE-TX-01 | Hard（A1）/ Medium（A2）/ Soft（B1 逆向自检） | typed-marker-funnel | A1：runtime/saga 生产文件里 StepFunc 调用仅限在 safeRun 内（typed callsite-uniqueness，Hard）；A2：safeRun 禁止出现在 RunInTx closure 内（Medium）；B1：禁止 StepFunc 类型别名（Soft，追踪升级） | runtime/saga, kernel/saga | 专属 |

---

### 批次 6 — Relay 生命周期隔离（RELAY-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| RELAY-NOT-MANAGEDRESOURCE-01 | Hard↓/Hard↑ | type-system-seal | *runtime/outbox.Relay 禁止实现 ManagedResource 接口，防止 WithManagedResource(relay) 绕过封装；via go/types.Implements | runtime/outbox, kernel/lifecycle | 专属 |
| RELAY-SOLE-HOLDER-01 | Hard↓/Hard↑ | single sanctioned holder | 满足 ManagedResource 且持有 *Relay 字段的 struct 仅限 runtime/bootstrap.relayAdapter，via RunTypedProduction 字段遍历 | runtime/outbox, runtime/bootstrap | 专属 |

---

### 批次 7 — 订阅身份约束（SUBSCRIPTION-*、REGISTRY-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SUBSCRIPTION-FIELDS-FROZEN-01 | Medium | reflect-field-freeze | kernel/outbox.Subscription 字段集冻结为 7 个，防止静默扩展改变 codegen 生成契约 | kernel/outbox | 专属 |
| SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01 | Medium | AST-pattern | ObservabilityID 方法体必须是单条 `return s.CellID`，禁止 CellID 为空时 fallback 到 ConsumerGroup | kernel/outbox | 专属 |
| REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01 | Medium | AST-pattern | Registrar.Subscribe 接口方法签名中 cellID 必须是第 4 个位置参数 string，防止被降级为可选 SubscriptionOption | kernel/cell | 专属 |

---

### 批次 8 — 合约 codegen 覆盖与 SafeID（EVENT-SUBSCRIPTION-*、SAFEID-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01 | Hard（推断） | codegen-funnel+golden | 每个 kind=event codegen=true 的 contract 必须有生成的 subscription_gen.go 且含 func NewSubscription | contracts/event, generated/ | 专属 |
| SAFEID-WIREMESSAGE-USAGE-01 | Hard↓ | string-typed-funnel | 所有使用 wireMessage 结构体字段的代码必须经过 outbox.SafeID 类型 funnel；via go/types 形态唯一性锁 | kernel/outbox | 专属 |
| SAFEID-UPSTREAM-FUNNEL-HARD-01 | Hard↑ | type-system-seal | wireMessage 及其字段全部 unexported，包外无法构造或作为 json.Unmarshal target，封闭上游 Hard | kernel/outbox | 专属 |

---

### 批次 9 — After-Commit Hook、Buffer-Then-Commit、L2 原子性（AFTERCOMMIT-*、VISIT-*、L2-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| AFTERCOMMIT-HOOK-PURE-TRANSIENT-01 | Hard（A1/A3↓）/ Medium（A2/A3↑） | typed-marker-funnel | A1：RegisterAfterCommit 入参必须是 func literal（Hard，形态唯一）；A2：hook 体禁止调用 pgx.Tx/sql.Tx/outbox.Writer 方法（Medium）；A3：调用方限五个 TxRunner 实现（Hard↓/Medium↑） | kernel/persistence | 半通用 |
| VISIT-BUFFER-THEN-COMMIT-01 | Medium（推断） | codegen-funnel+golden | generated/contracts 下 types_gen.go 的 Visit 方法必须先 json.Encode 到 bytes.Buffer 再 WriteHeader，防止 commit-then-stream 反模式 | generated/contracts | 半通用 |
| L2-OUTBOX-ATOMICITY-COVERAGE-01 | Hard（主路径）/ Medium（B1/B2/B4/B5 盲区） | codegen-funnel+golden | 每个 L2 slice 的 Service 必须有对应的 L2 原子性测试函数（TestXxxL2OutboxAtomicity），via YAML 扫描 + go/types 派生 + AST 名称匹配三层 Hard | cells/**/slices, kernel/outbox | 专属 |

---

### 批次 10 — 健康聚合与事件命名（HEALTH-*、EVENT-*）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| HEALTH-AGG-01 | Medium（推断） | conformance-test | runtime/和 adapters/ 中暴露 Checkers()/HealthCheckers() 的导出类型必须同时实现 ManagedResource（含 Worker+Close），via go/types 方法集推导（含 embedding） | runtime/*, adapters/* | 半通用 |
| EVENT-PAYLOAD-CAMELCASE-01 | Medium（推断） | metadata/yaml-derive | contracts/event/**/payload.schema.json 的顶层属性名禁止包含下划线，必须使用 camelCase | contracts/event | 通用 |
| EVENT-DTO-CAMELCASE-01 | Medium（推断） | AST-pattern | cells/**/dto/*event*.go 中结构体 json tag 字段名禁止包含下划线，与事件 payload schema 保持一致 | cells/**/dto | 通用 |
