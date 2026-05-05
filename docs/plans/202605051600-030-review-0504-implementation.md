# 030 · GoCell 0504 Review 实施计划（独立于 027/028/029）

> 日期：2026-05-05
> 来源：`docs/reviews/20260504-*.md`（16 份，约 120 条 finding 去重后 ≈ 80）+ `docs/reviews/202605041800-systems-engineering-gap-assessment.md`
> 形态：架构边界仲裁前置 + 高杠杆主轴串行 + 防腐与工程基线并行
> 不依赖 029 在飞 PR；与 029 共用 reviewer 容量需在飞 PR 总数 ≤ 4

---

## 0. Lane 通道

| Lane | 覆盖 | 主文件域 | 并发上限 |
|---|---|---|---|
| **K** Critical | P0 race + 边界 ADR + cell.yaml 单源 + 概念收敛 | `kernel/assembly/*` + `kernel/cell/*` + `kernel/metadata/*` + `kernel/wrapper/*` + `cells/*/cell.yaml`/`cell.go` + ADR | 1 |
| **R** Runtime/Observability | 事件路径 metrics 补齐 + bootstrap 自动 wire + GaugeVec | `runtime/observability/metrics/*` + `runtime/eventrouter/*` + `runtime/eventbus/*` + `runtime/outbox/relay.go` + `kernel/observability/metrics/*` | 1 |
| **A** Adapters | OIDC fail-fast / ManagedResource / 错误分类 / fake 导出 | `adapters/{oidc,postgres,redis,s3,circuitbreaker,rabbitmq}/*` + `tools/archtest/managed_resource_test.go` | 1 |
| **C** Cells | cells 治理收敛（Init 模板 + slice 拆 + cache 生命周期 + identitymanage L 级） | `cells/{accesscore,auditcore,configcore}/*` + `kernel/cell/registry_template.go` | 1 |
| **G** Governance/Kernel 防腐 | hookDispatcher / rollback ctx / Validate(ctx) / scaffold 注入 / crypto 接口 | `kernel/assembly/hook_dispatcher.go` + `kernel/governance/*` + `kernel/scaffold/*` + `kernel/crypto/*` | 1 |
| **F** Foundations / 工程基线 | CODEOWNERS / Makefile / 需求追溯 / SysML 视图 / 状态机显式化 | `.github/*` + `Makefile` + `docs/requirements/*` + `tools/sysmlgen/*` + `kernel/cell/lifecycle.go` | 1 |

**总并发**：6 lane 上限；与 029 在飞合计 ≤ 4。

**冲突避让**：
- **K-06 cell.yaml 单源** vs **C lane**：触全 cell `cell.yaml` + `cell.go`，C lane 期间须暂停；K-06 合入后 C lane 解封
- **K-04/05 ADR 仲裁** vs **A/R lane**：仲裁结果会改动 contract kind 列表与 ContractMeta 形状，影响 R/A 中 metrics 命名与 collector 接口；ADR 落 develop 之前 R 不动 outbox/event metric 命名
- **G-11 scaffold 自由文本注入** vs **K-06 scaffold 模板升级**：两者改 `scaffold/scaffold.go` 与模板，须串行
- **F-01 CODEOWNERS / F-02 Makefile target** 与所有 lane 文件域 0 重叠，可全程并行

---

## 1. 优先实施路径（Phase）

| Phase | 目标 | Lane K | Lane G | 并行 | 估时 |
|:-:|---|---|---|---|---|
| **0** | P0 race + ADR 仲裁前置 | K-01 / K-02 / K-03 / **K-04 + K-05 ADR 起草** | — | — | ~3 天 |
| **1** | 架构边界仲裁定稿 | K-04 / K-05 ADR 落 develop | — | F-01（CODEOWNERS/PR 模板）+ F-02（Makefile target）独立 | ~1 周 |
| **2** | cell 单源 + 概念收敛实施 | K-06 / K-07 / K-08 | — | A-01..A-04（adapters 不触 cells/）| ~2 周 |
| **3** | 事件可观测性 + cells 收敛 | — | R-01..R-03 + C-01..C-05 | G-01..G-04（kernel 防腐）+ A-05..A-08 | ~3 周 |
| **4** | governance ctx + 装备类 + 路线图扩展 | — | G-05..G-15 | J-01..J-04 + F-03..F-10（按 reviewer 容量）| ~3 周 |

**累计**：~9 周单线 / ~5-6 周双 lane（K + G 并行 + F 异步穿插）。

**为什么 Phase 0 必须先做 ADR**：
- K-04（platform cells 边界）+ K-05（ContractMeta SRP / 4-kind 必然性）的决定结果会决定 K-06 触多少 cell、改多少 contract 字段；先实施再 ADR 会双重返工
- K-01 race 修复 5 行成本，与 ADR 无关，可同 PR 起草

---

## 2. 关键路径（11 项 / ~178h dev + 86h review）

| # | PR | 来源 | 问题 | 方案 | ship | 工时 | 依赖 |
|---|---|---|---|---|:-:|---|---|
| K-01 | KERNEL-ASSEMBLY-SNAPSHOTS-RACE-FIX | kernel-group1 G1-01（P0）| `assembly.startInternal` Phase1 在 Init 循环内裸写 `a.snapshots`，与 `Snapshots()` 持锁读形成 fatal map race，进程不可 recover | Init 循环写局部 `localSnaps`，全部成功后在 `a.mu` 锁内一次性赋值；新增 `TestAssembly_StartConcurrentSnapshots_RaceDetector` 锁定 | L1 | 4h + 2h | - |
| K-02 | JOURNEY-LIFECYCLE-CI-CLOSE | journeys-06 P0 + summary #1/#8 | 8 条 J-*.yaml 全部 `lifecycle: experimental`，`gocell verify journey --active` 静默跳过；J-confighotreload 引用未声明的 `event.config.entry-deleted.v1` | (a) 升 J-ssologin 为 active；(b) `runner.RunActiveJourneys` active 集为空时 fail；(c) `gocell validate` 增 `journey.contracts ↔ contracts/` 双向存在性校验（对偶 ADV-06） | L1 | 6h + 3h | - |
| K-03 | KERNEL-OBSERVABILITY-PKGDOC | kernel-01 P1#2 | `kernel/observability/` 无包级 doc.go，与 `runtime/observability/` 职责切分不明 | 30-50 行 `doc.go` 明确"kernel/observability 只定义 provider-neutral 抽象，导出器在 adapters/runtime"；同时为 K-04/05 ADR 编辑前提 | L1 | 2h + 1h | - |
| K-04 | ADR-PLATFORM-CELLS-BOUNDARY | software-review §3.1（最大违例）| `cells/{accesscore,auditcore,configcore}` 是否应留在 framework 仓 — 客户做 IoT/order/BFF 用不到却必须 vendor；`corebundle` 默认带这 3 个让"框架"和"v1 平台"边界模糊 | architect 仲裁 ADR：(a) 留下 + 明确 corebundle 是「参考 assembly」非框架契约；或 (b) 物理迁 `examples/platform-cells/` 或独立仓；ADR 必须给出 archtest 守卫方案与 examples/ssobff 影响面评估 | L2 ADR-only | 12h + 6h | K-03 |
| K-05 | ADR-CONTRACT-CONCEPT-COLLAPSE | kernel-01 P1#3 + contracts-05 P1#1+P1#2 + summary #3 + software-review §2.1 | 同一 contract 概念 3 处分散：`metadata.ContractMeta`（治理）+ `wrapper.ContractSpec`（运行时）+ `EndpointsMeta` 10 字段 omitempty 森林；4 类 kind 中 command/projection 在平台层 0 实例（F1×F2 推 2 类即覆盖） | architect 仲裁 ADR：(a) 4-kind 必然性论证 — 保留 4 / 收敛到 (Sync, Persistent) 二维 / 把 command+projection 标 reserved；(b) ContractMeta 拆为 sealed `oneof HTTPEndpoints / EventEndpoints / ...`；(c) ContractSpec 派生自 ContractMeta（runtime 投影）。**K-05a** ✅ CONTRACT-KINDS-CLOSED-SET-01 archtest landed in PR-V1-CODEGEN-FULL-MIGRATION W3.0. **K-05c** ✅ 3 archtest gates GREEN (CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 / NO-MANUAL-CONTRACTSPEC-LITERAL-01 / EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01). | L2 ADR-only | 16h + 8h | K-03 |
| K-06 | ✅ CELL-METADATA-SINGLE-SOURCE (残余 PR-V1-CODEGEN-FULL-MIGRATION W4) | cells-04 P0 + summary #2 | accesscore/auditcore/configcore 的 cell.yaml `Owner.Role/Schema/Smoke` 与 `NewBaseCell(CellMetadata{...})` 字面量四处漂移；configcore 顶层缺 `consistencyLevel` | `NewXxxCore` 改为接受外部加载的 `CellMetadata`（`//go:embed cell.yaml` 自填）；scaffold 模板同步升级；新增 archtest `CELL-YAML-GO-PARITY-01` 双向校验；3 cell 全量迁 + corebundle 适配。**PR-V1-CODEGEN-FULL-MIGRATION W4 残余已完成**：configcore.cell.yaml `consistencyLevel: L2` 已锁定 + scaffold golden test 已加入 `cmd/gocell/app/scaffold_golden_test.go` (2 table cases: GoldenCellGo + GoldenCellYAML). | ✅ | 24h + 12h | K-04 K-05 |
| K-07 | CELLS-SLICE-MULTI-VERB-DECOMPOSE | cells-04 P1 + summary #7 | `auditappend` 单 slice 订阅 13 + publish 1 = 14 contractUsages；`configread` 单 slice 同时挂 PrimaryListener + InternalListener 两个端口面 | 按事件域拆 `auditappend-{session,user,config,role}`，共享 service.HandleEvent dispatch；`configread` 拆 `configread-internal`；slice.yaml `verify.contract` 各自承担；不留兼容包装 | L3 | 20h + 10h | K-06 |
| K-08 | ASSEMBLY-SCHEMA-SCAFFOLD-EXPAND | assemblies-07 P1 + summary #6 | (a) `AssemblyMeta` 缺 owner / maxConsistencyLevel / deployTemplate enum；(b) `gocell scaffold` 不支持 assembly；(c) `cmd/corebundle/run.go` corebundleModules switch 是 yaml 平行的手工映射表 | (1) schema 加 `owner`(必填) + `maxConsistencyLevel`(派生校验) + `validDeployTemplates={k8s,compose,binary}` enum；(2) `gocell scaffold assembly --id=... --cells=... --deploy=k8s`；(3) `gocell generate assembly` 派生 `cmd/{id}/modules_gen.go`（cell ID → CellModule 工厂），run.go 只留环境加载 | L3 | 20h + 10h | K-04 |
| R-01 | EVENT-OBSERVABILITY-METRIC-PACK | runtime-02 P1×4 + kernel-group2 G2-01/G2-02 + kernel-group3 G3-11 + R-04 | (a) `outbox.RelayCollector` 不被 bootstrap 自动注入；(b) `eventrouter.Router` 完全无 collector；(c) `InMemoryEventBus` drop 仅 Warn 无 counter；(d) `runtime/observability/metrics` 缺 outbox/event 命名空间；(e) `kernel/observability/metrics.Provider` 无 `GaugeVec`；(f) relay pending depth 无 Gauge；(g) consumer reject 无 counter | (1) `Provider` 加 `GaugeVec` + NopProvider 实现；(2) shutdown/outbox/event 三套 collector 工厂收口到 `runtime/observability/metrics/{shutdown,outbox,event}.go`；(3) bootstrap phase 5/6 用 `metricsProvider` 自动 wire；(4) 新增 `event_router_{subscriptions_active,setup_errors_total,ready_wait_seconds}` + `outbox_pending_depth` + `outbox_consumer_rejected_total{cell,topic,reason}` + `eventbus_dropped_total{reason}`；(5) consumer reject 日志升 Error 级 | L3 | 20h + 10h | K-05 |
| R-02 | EVENTBUS-DROP-CONTEXTUAL-LOG | runtime-02 P2 | `InMemoryEventBus.broadcast/roundRobin` drop 路径 slog.Warn 缺 entry_id/aggregate_id/event_type；违反 observability.md「错误日志必须含结构化关联字段」 | 升 Error 级 + 三字段；与 R-01 counter 对应 | L1 | 2h + 1h | R-01 |
| R-03 | BOOTSTRAP-NIL-OPTION-CONSISTENCY | runtime-02 P2 | `WithManagedCloser(nil)` 静默接受，`WithManagedResource(nil)` phase0 fail-fast — 相邻 API 风格冲突 | 两者均改 fail-fast；option 函数记录 nil 标志，phase0 拒绝 | L1 | 2h + 1h | - |
| A-01 | OIDC-FAILFAST-MR-COMPLETENESS | adapters-03 P0 + summary #4 | (a) `oidc.New` 不连 issuer，discovery 推迟到首请求；(b) postgres/redis/s3/oidc 未实现 `lifecycle.ManagedResource.Checkers`，readyz 缺位；(c) s3.Health 是 HeadBucket（每次探针打 S3）| (1) OIDC `New(ctx, cfg)` 期同步执行 `discover(ctx, true)`；(2) 4 adapter 实现 `Checkers()` 返回 `{name}_ready`；(3) s3 改"状态机 + 后台 health-check goroutine"，probe 只读最新结果；(4) 新增 archtest `MANAGED-RESOURCE-COMPLETENESS-01` 守卫所有外部依赖 adapter 必实现 ManagedResource | L3 | 24h + 12h | - |
| A-02 | OIDC-JWKS-ROTATION-WORKER | adapters-03 P1 | OIDC provider cache 永不过期，注释把刷新责任甩给 caller，IdP 轮换 JWKS 全员鉴权失败 | adapter 内置 `tokenRenewalWorker`，遵循 OIDC `cache_max_age` 头（fallback 24h）；通过 `ManagedResource.Worker()` 暴露 | L2 | 8h + 4h | A-01 |
| A-03 | ADAPTER-ERROR-CLASSIFICATION-TRANSIENT | adapters-03 P1 | postgres/redis/s3 错误码不分 transient/permanent，consumer 无法做退避决策 | 复用 `errcode.WrapInfra` + `errcode.IsTransient`（vault 已是范例）；PG `40001`/`40P01`/`08*`、Redis `i/o timeout`、S3 5xx/429 标记 transient；其余永久 | L3 | 16h + 8h | A-01 |
| A-04 | ADAPTER-FAKE-EXPORT | adapters-03 P1 | adapter fake 仅在 `_test.go` 内 white-box，cells 测试只能自写 fake 或 import adapter（破 LAYER-04） | 每个对外接口 adapter 开 `adapters/<name>/<name>fake/` 子包导出 `NewFakeClient/NewMemKeyProvider`；`runtime/eventbus` in-mem 模式参考 | L2 | 12h + 6h | A-01 |
| C-01 | CONFIGCORE-RECEIVE-PLACEHOLDER-CLEAR | cells-04 P1 | `accesscore/configreceive` 自承"placeholder per ADV-05"，被强行钉占位让 ADV-05 不报错 | 让 configcore 的 entry-upserted/deleted contract 标 `lifecycle: draft` 直到真有消费动机；删 configreceive；不维持占位绕过治理 | L2 | 8h + 4h | K-06 |
| C-02 | CONFIGSUBSCRIBE-CACHE-LIFECYCLE | cells-04 P1 | `configsubscribe.Cache` 进程内无界 + 未挂 Lifecycle，长寿进程内存增长无界 | 挂 `kernel/cell.LifecycleHook`：OnStart hydrate / OnStop snapshot；改 LRU + size cap；暴露 `eventbus_cache_size` metric | L2 | 8h + 4h | K-06 |
| C-03 | CELLS-IDENTITYMANAGE-L-LEVEL-FIX | cells-04 P1 | identitymanage 标 L1 但 publish 5 个 user.* L2 事件 | `AddSlice(... cell.L2)`；同审 8 处 `NewBaseSlice` L 字面量是否反映真实 contractUsages 角色 | L1 | 3h + 2h | K-06 |
| C-04 | CELLS-INIT-TEMPLATE-CONVERGE | cells-04 P2 + cells-04 P2 internal/ 不对称 | 3 cell Init 切分各异（accesscore 7 helper / auditcore 5 / configcore 6 init*Slice），第 4 cell 无标准；internal/ 子包数量在 3 cell 间高度不对称 | `kernel/cell` 提供 `BaseCell.RegisterStandard(reg, StandardInit{Slices, RouteGroups, Subscriptions, Health, Lifecycle})` 模板；scaffold 模板预生成 `internal/{ports,domain,dto,events,mem}` 五目录；3 cell 改造 + scaffold 升级 | L2 | 12h + 6h | K-06 |
| C-05 | CELLS-CELLROUTES-PLACEHOLDER-DELETE | cells-04 P2 | `configcore/cell_routes.go` 退化为占位文件（仅"intentionally empty after Batch 3 migration" 注释），项目无外部消费方理由保留 | 直接删除文件；迁移上下文挪到 commit message | L1 | 0.5h + 0.5h | - |
| G-01 | KERNEL-HOOK-DISPATCHER-LIFECYCLE | kernel-group1 G1-02/G1-08/G1-13 | (a) `dispatchOne` 超时后遗弃 goroutine，`d.wg` 不追踪，`stop()` 后孤儿 goroutine 永久存活；(b) `slog.Any("panic", r)` 泄漏 observer panic value；(c) `queue_full` drop 仅 metric counter，无 slog 兜底 | (1) 加 `d.sinkWg`，`stop()` drain 后 `sinkWg.Wait()` 兜底；(2) `fmt.Sprintf("%v", r)` + 截断 256 字节；(3) `queue_full` 分支补 `slog.Warn` 回退 | L2 | 8h + 4h | - |
| G-02 | KERNEL-ASSEMBLY-ROLLBACK-CTX-DECOUPLE | kernel-group1 G1-03/G1-04 | (a) 启动期 SIGTERM → `rollbackCells(ctx,...)` 用已 cancelled ctx，BeforeStop/Stop/AfterStop 拿到立即 done 的 context；(b) shutdownTimeout=30s 与 k8s `terminationGracePeriodSeconds` 默认 30s 无安全余量 | (1) `rollbackCells(context.WithTimeout(context.Background(), cfg.HookTimeout), upTo)`；(2) `phase0ValidateOptions` warn `terminationGracePeriodSeconds >= shutdownTimeout + preShutdownDelay + 10s`；ADR 同步部署文档 | L2 | 8h + 4h | - |
| G-03 | GOVERNANCE-VALIDATE-CTX-PROPAGATION | kernel-group3 G3-02/G3-03/G3-04 | (a) `Validator.Validate()` 不接 ctx，VERIFY-06 用 `context.Background()` 调 `go test`，CI `--strict` 卡死永久阻塞；(b) `runGit()` 硬编码 `context.Background()`，NFS/FUSE 永久阻塞；(c) `ValidateFailFast()` 整函数零测试覆盖 | (1) `Validate(ctx context.Context)` 全链路透传；(2) `runGit(ctx, args...)`；(3) 新增 `TestValidateFailFast_ShortCircuitsOnFirstError` | L2 | 12h + 6h | - |
| G-04 | KERNEL-INTERNAL-DAG-GUARD | kernel-01 P1#1 | `depguard kernel-isolation` 把 kernel 当一个整体黑盒，22 个子模块之间无静态 DAG 约束；若 `crypto` 反向 import `assembly`，CI 不拦截；kernel 是底座，DAG 反转是高杠杆失误 | `tools/archtest/` 新增 `KERNEL-INTERNAL-DAG-01`：固化已知合法上游（assembly/wrapper 顶层、crypto/clock/ctxkeys 叶子）；与 K-03 一起作为 kernel 内部边界双护栏 | L2 | 8h + 4h | K-03 |

---

## 3. 并行轨道

### Track A · Adapters 后续（4 PR / 12h + 6h）

| # | PR | 来源 | 问题 | 方案 | ship | 工时 |
|---|---|---|---|---|:-:|---|
| A-05 | ✅ PR-V1-CIRCUITBREAKER-INHOUSE-ERRCODE（refactor/524-circuitbreaker-inhouse）| adapters-03 P1 | `circuitbreaker.New` 用 `fmt.Errorf` 而非 errcode；sony/gobreaker/v2 第三方依赖可替换；`time.Now()` 直调违反 PROD-CLOCK-INJECTION-01 | ✅ shipped: 自写 ~200 LOC generation+expiry 状态机替换 sony/gobreaker/v2；定义 `ErrAdapterCircuitBreakerConfig errcode.Code` + `errcode.New(KindInvalid, ...)`；注入 `kernel/clock.Clock`（PROD-CLOCK-INJECTION-01）；11 现有测试改造（4 个 Eventually → clockmock.Advance deterministic）+ 8 个新分支测试（generation/interval/half-open/IsSuccessful 等）；go mod tidy 删 sony/gobreaker/v2 | ✅ done（PR 待补 URL）| 8h + 4h |
| A-06 | ✅ **吸收进 PR-V1-RMQ-TERMINAL（029 A4）** RMQ-WAITCONNECTED-DOC-FIX | adapters-03 P2 | `MaxReconnectAttempts` 字段标 ignored，但 `WaitConnected` godoc 仍列 `ErrAdapterAMQPReconnectExhausted` | 同 029 A4 一并删字段 + errcode + godoc，并 runtime reconnect 重新分类 broker permanent → StateTerminal | ✅ done | — |
| A-07 | POSTGRES-POOL-MANAGED-RESOURCE | adapters-03 P2 | `postgres.Pool` 仅满足 `ContextCloser`，与 outbox relay 后台需求一致性差 | 升级 `Pool` 到 `ManagedResource(Checkers + Worker=nil)` 或在 doc.go 写明"Pool 是 ContextCloser，无需 Worker" | L1 | 4h + 2h |
| A-08 | ADAPTERUTIL-HEALTH-WRAPPER | adapters-03 §3 跨层观察 | `Health → Checkers map`、`Status → metric` 转换在多 adapter 重复 | 下沉到 `adapters/adapterutil/`，对偶 `CloseWithDeadline` | L1 | 4h + 2h |

### Track C · Cells 后续（2 PR / 6h + 3h）

| # | PR | 来源 | 问题 | 方案 | ship | 工时 |
|---|---|---|---|---|:-:|---|
| C-06 | L0-CELL-DECISION | cells-04 P2 | `l0Dependencies: []` 在 3 cell 全空，无任何 `type: l0` 实例，schema 字段是死代码路径 | scaffold 命令对外承诺 vs 现实二选一：(a) 升 `pkg/query.CursorCodec` 等共享逻辑为示例 L0 cell 验证通路；(b) 文档明确"L0 cell 是未来扩展点，当前无实例" | L1 | 2h + 1h |
| C-07 | EMITTER-HEALTH-PROBE-HELPER | cells-04 §3 跨层观察 | `cell.HealthProber` 在 3 cell 重复 4 次（`if hc, ok := c.emitter.(cell.HealthProber); ok`） | 抽 `cell.RegisterEmitterHealthProbes(reg, emitter)` helper | L1 | 4h + 2h |

### Track G · Kernel 三组防腐与质量后续（11 PR / 50h + 24h）

| # | PR | 来源 | 问题 | 方案 | ship | 工时 |
|---|---|---|---|---|:-:|---|
| G-05 | OUTBOX-CONSUMER-COLLECTOR-INTERFACE | kernel-group2 G2-02 | retry budget 耗尽 reject 路径仅 slog.Warn，无 counter；违反 observability.md「影响正确性 → Error 级」 | 新增 `ConsumerCollector` 接口（含 `RecordRejected(reason)`），由 R-01 注入；日志升 Error 级 | 与 R-01 同 PR 落 | — |
| G-06 | OUTBOX-PAYLOAD-MAX-SIZE | kernel-group2 G2-04 | `Entry.Payload []byte` 无上限校验；超大 payload 致 DB 行过大、relay OOM、consumer OOM | `MaxPayloadSize=512 KiB` 常量 + `Entry.Validate()` 校验；与 NATS JetStream 默认 1 MiB 对齐 | L1 | 2h + 1h |
| G-07 | OUTBOX-WRITER-MUST-CONTRACT | kernel-group2 G2-09/G2-10/G2-11/G2-13 | (a) `Writer.Write` 注释 SHOULD 而非 MUST 参与事务；(b) outbox/command 中 `MaxMetadataKeys` 等校验完全重复；(c) `HandleResult.Receipt` 是 exported 但禁止 handler 读写；(d) 缺 `Ack()/Requeue(err)/Reject(err)` 工厂 | (1) 改 MUST + `TxRunner.RunInTx` godoc 强制；(2) 提取 `kernel/metautil`；(3) `Receipt` 改 unexported 或移 internal；(4) 提供工厂函数 | L2 | 8h + 4h |
| G-08 | OUTBOX-FAILOPEN-COUNTER + INMEM-RECEIPT-FIX | kernel-group2 G2-06/G2-07/G2-08 | (a) fail-open `RecordDrop()` 无 metrics；(b) `inMemReceipt.Commit/Release` 共享 `sync.Once`，Release 先于 Commit 静默 false-success；(c) `UnmarshalEnvelope` `msg.ID` 仅非空检查，可日志注入（CWE-117） | (1) `RecordDrop()` increment `outbox_failopen_drops_total{cell}`；(2) `committed atomic.Bool` 区分 release vs commit；(3) 复用 `idutil.IsSafeID` | L2 | 6h + 3h |
| G-09 | COMMAND-SWEEPER-PRODUCTION-GUARD | kernel-group2 G2-03/G2-19 | (a) `Sweeper.OnError=nil` 时 sweep 失败完全沉默；(b) Sweeper 用公开字段 + `Start()` 运行时 nil 检查，与项目 fail-fast 构造器约定不一致 | (1) `runTick` 错误分支补 `slog.Error` + `command_sweep_errors_total`；(2) `NewSweeper(scanner, queue, clk, ...)` 构造器构造期 fail-fast | L2 | 6h + 3h |
| G-10 | KERNEL-CELL-PACKAGE-DECOMPOSE | kernel-group1 G1-05/G1-06/G1-10/G1-18 | `kernel/cell` 是 god-package：含 AuthPlan(JWT/MTLS) + Outbox EmitterFactory + Health alias；`Cell` 接口 11 方法混合生命周期与元数据自省；3 个 "registry" 命名混乱；`mode_resolver.go` 文件名与内容不符 | (1) `auth_plan.go` → `kernel/auth/`；(2) `mode_resolver.go` → `kernel/outbox/` + 改名 `emitter_resolver.go`；(3) `cell.Registry` → `cell.Registrar`，`kernel/registry.CellRegistry` → `kernel/registry.CellIndex`；(4) `Cell` 拆 `CellLifecycle` + `CellDescriptor` 嵌入；删 `health.go` 单行 alias | L3 | 16h + 8h |
| G-11 | SCAFFOLD-FREETEXT-YAML-INJECTION | kernel-group3 G3-08/G3-18 | `Goal` / `OwnerTeam` 自由文本写入 YAML 无字符过滤；`\n` 注入产生额外键，绕过 VERIFY/FMT 规则前提 | `validateFreeText()` 拒绝 `\n\r":#[]{}` 等；模板裸 scalar 改单引号包裹；新增 `TestCreateJourney_YAMLInjection` 对抗测试 | L2 | 6h + 3h |
| G-12 | CRYPTO-INTERFACE-HARDENING | kernel-group3 G3-09/G3-10/G3-13/G3-19 | (a) `MatchKeyID` 普通字符串比较，时序侧信道；(b) `KeyHandle.Encrypt` MUST nonce 唯一无 contract test；(c) `KeyHandle.Encrypt` vs `ValueTransformer.Encrypt` 返回值顺序漂移（nonce/keyID 互换） | (1) `crypto/subtle.ConstantTimeCompare`；(2) `TestKeyHandle_NonceUniqueness` contract test；(3) 引入 `EncryptResult { Ciphertext, Nonce, EDK []byte; KeyID string }` struct 统一两接口签名 | L2 | 8h + 4h |
| G-13 | GOVERNANCE-RULES-REGISTRATION-GUARD | kernel-group3 G3-05/G3-06/G3-15/G3-20 | (a) `Validator.rules()` 手工 slice，漏注册零反馈；(b) `ValidateStrict` / `ValidateStrictFailFast` 双列表漂移；(c) error 规则无修复指导；(d) rule code 字面量散落 | (1) archtest 反射枚举 `Validator` 上 `func() []ValidationResult` 方法 vs `rules()` 长度对比；(2) 统一 `ValidateStrict(strict, failFast bool)` 单入口；(3) error 规则参照 ADV-06 追加 `; fix: ...`；(4) 提取 `rulecodes.go` 常量文件 | L2 | 12h + 6h |
| G-14 | VERIFY-PRINTER-ZEROMATCH-WARN | kernel-group3 G3-16 | text printer 对 `TestResult.ZeroMatch=true` 无警告，与 `[PASS]` + 实际跑 N 个测试输出完全相同 | `printTestResults` 检测 `tr.ZeroMatch` 输出 `[WARN] %s — no tests matched -run pattern` | L1 | 1h + 1h |
| G-15 | KERNEL-METADATA-CODEGEN-OVERLAY | kernel-01 P2 | `kernel/metadata` 既是被 governance 校验的"被动数据结构"，又承载 `goStructName` 等 codegen-only 字段，破坏"kernel 不知道 codegen"公理 | 把 codegen-only 字段挪到 `tools/codegen` schema overlay；或在 `metadata/doc.go` 注明 metadata 包是"YAML schema 总账本"故意承载多消费方所需字段 | L2 | 4h + 2h |

### Track J · Journeys & Contracts 收敛（4 PR / 18h + 9h）

| # | PR | 来源 | 问题 | 方案 | ship | 工时 |
|---|---|---|---|---|:-:|---|
| J-01 | JOURNEY-STATUS-BOARD-CONSISTENCY | journeys-06 P1 | (a) `status-board.yaml` `state: doing/todo` 与 J-*.yaml `lifecycle: experimental` 双轨无约束；(b) J-ordercreate 在 board 占位但无对应 yaml | 定义状态机 `todo→doing→done` ↔ `experimental→active→stable` 强映射；validate 双向校验 + `status-board.journeyId ⊆ journeys/J-*.yaml`；J-ordercreate 移到独立 `roadmap.yaml` 或落地 yaml | L2 | 6h + 3h |
| J-02 | JOURNEYS-FIXTURES-DECISION | journeys-06 P1 + R-03 | `fixtures/` 仅 `.gitkeep`，CLAUDE.md 声明"供 run-journey 使用"但 schema 无 fixtures 字段 | 二选一：(a) 删除 `fixtures/` + 撤回 CLAUDE.md 引用；(b) 引入 `fixtures: [fixture-id]` 字段 + runner 注入机制 | L1 | 2h + 1h |
| J-03 | CONTRACT-V1V2-DRY-RUN | contracts-05 P1#3 | api-versioning.md 写 v2 规则但 0 实例、0 deprecation 模板、无 v1/v2 共存示例 | 选 contract（如 audit list）走一遍 v1→v2 演练：目录约定 + ContractMeta.id 命名 + ownerCell 双挂 + lifecycle (`deprecated` vs `superseded`) + outbox triggers + journey checkRef 平滑迁移；同步加 ADR；或写 ADR 明确"1.0 之前不做 v2 升级"并删 api-versioning.md v2 段落 | L2 | 8h + 4h |
| J-04 | CONTRACT-SCHEMA-NAMING-NORMALIZE | contracts-05 P2×2 | (a) api-versioning.md 写 `pageSize`，contract 实际用 `limit`（规则与代码漂移）；(b) event headers `event_id`(snake_case) 与 cell-patterns.md "camelCase" 冲突 | (a) 改规则文档 `pageSize → limit`（按 MEMORY 规则不超前于代码库）；(b) 与 J-03 v1→v2 演练搭车统一 envelope 字段；或写明 envelope headers 沿用 outbox transport 字段命名 | L1 | 4h + 1h |

### Track F · 工程基线 + 路线图扩展（10 PR / 78h + 38h）

| # | PR | 来源 | 问题 | 方案 | ship | 工时 |
|---|---|---|---|---|:-:|---|
| F-01 | CODEOWNERS-PR-TEMPLATE | supporting-08 P1 + R-08 | `.github/CODEOWNERS` + `pull_request_template.md` 不存在；reviewer 路由全靠手动；PR 描述无强制 `ref:` / 一致性级别 / journey 影响面 | 新建 CODEOWNERS（`/kernel/ @owner-kernel` 等粒度）+ `.github/pull_request_template.md`（含 ref / 一致性级别 / 影响 journey / archtest 规则增量 4 项 checklist）+ branch protection 配置文件 | L1 | 4h + 2h |
| F-02 | MAKEFILE-LINT-RACE-ARCHTEST | supporting-08 P1 | Makefile 13 target 缺 `lint` / `race` / `archtest` 独立 target；CI 与本地命令漂移；lint exclusions 13 条无周期复盘机制 | (1) `make lint`（直调 `golangci-lint run`）+ `make race`（镜像 test-race.yml 包列表）+ `make archtest`；(2) CI yaml 改调 Makefile target；(3) `hack/verify-lint-exclusions.sh` 校验每条 exclusion 含 `# R2-DECIDED: yyyy-mm` 时间戳 | L2 | 8h + 4h |
| F-03 | PKG-CONTRACTS-BOUNDARY-DOC + ARCHTEST | supporting-08 P1 + supporting-08 §3 跨层观察 | `pkg/contracts` 角色未在 README/doc.go 说明，未来若放业务领域类型，archtest 不会立即报；`pkg/ctxkeys` 与 `kernel/ctxkeys` 边界微妙 | (1) 新增 `pkg/contracts/doc.go`：明确"仅承载 contracts/*.yaml Go 类型镜像 + Schema helper，禁业务领域类型"；(2) archtest `PKG-CONTRACTS-NO-BUSINESS-TYPE` + `PKG-CTXKEYS-NO-CELL-MODEL` | L2 | 6h + 3h |
| F-04 | CMD-GOCELL-VS-COREBUNDLE-DOC | supporting-08 P2 | `cmd/CLAUDE.md` 主题是 corebundle 三层组装，对 `cmd/gocell` 在 Composition Root 中地位完全没着墨 | 文首加对照段：`cmd/gocell` = 治理/元数据/生成器 CLI（开发期+CI）；`cmd/corebundle` = `assemblies/corebundle/` 的运行时组装产物 | L1 | 1h + 1h |
| F-05 | QODANA-WORKFLOW-AUDIT | supporting-08 P2 | Qodana 与 CodeQL/Semgrep 双重覆盖、增量价值未在 yaml 注释说明；`pr-mode: false` 不阻断 PR | 二选一：(a) 补 yaml 头部注释明确差异化覆盖；(b) retire 该 workflow + 删 `QODANA_TOKEN_1820249425` secret | L1 | 2h + 1h |
| F-06 | REQUIREMENTS-TRACEABILITY-CHAIN | gap-assessment R-01 | 无 `docs/requirements/` 目录；ADR/Roadmap/journey goal 三处隐含需求；contract.yaml/journey 无 `requirementID` 反向链；V 模型左侧追溯断点 | 引入 `docs/requirements/REQ-*.yaml`（id/text/category/priority/satisfiedBy/verify）；contract.yaml + journey schema 加 `requirementID: []`；archtest `REQ-TRACE-01` 双向校验；1-2 篇 ADR | L3 | 24h + 12h |
| F-07 | SYSML-VIEW-CODEGEN | gap-assessment R-05 | 5 张 SysML 图（BDD/IBD/用例/活动/状态机）有元数据天然映射但无生成器 | 新建 `tools/sysmlgen/`：cell.yaml/slice.yaml/contract.yaml/journey.yaml/assembly.yaml → `generated/sysml/<view>.{puml,mermaid}`；CI step `make sysml-verify` 校验产物与 yaml 同步 | L3 | 16h + 8h |
| F-08 | LIFECYCLE-STATE-MACHINE-EXPLICIT | gap-assessment R-06 | `cell.lifecycle` 与 `outbox.entry.state` 隐含状态机；无 enum + transition 表 | (1) `kernel/cell/lifecycle.go` 显式 `state enum + transition map`；(2) `kernel/outbox/state.go` 同款；(3) archtest 校验状态转移完备性；(4) 1 篇 ADR | L3 | 12h + 6h |
| F-09 | CONSTRAINTS-PARAMETRIC-FIELD | gap-assessment R-07 | cell.yaml 无 `constraints` 字段；SLO/性能/容量约束写在 PR 描述而非模型 | 加 `constraints: { latency: {p99_ms, p999_ms}, throughput: {publish_per_second}, capacity: {queue_depth_max} }`；verify 钩子跑 micro-benchmark 校验 | L3 | 12h + 6h |
| F-10 | ADR-INDEX-LANDED | gap-assessment R-09 + commit `11600a4f` 提到 ADR-INDEX-01 但 `docs/architecture/` 内未发现 | ADR-INDEX-01 是否已落地为 `docs/architecture/ADR-INDEX.md`？若未落地则补齐；建立 ADR ↔ K#xx/J#xx/D#xx 任务条目双向链接 | L1 | 3h + 2h |

---

## 4. Won't-do / 触发条件待达

| 来源 | 项 | 理由 / 触发条件 |
|---|---|---|
| summary §5 | CD 链路 / 镜像 / SBOM / staging / canary | GoCell 是嵌入式框架，不拥有运行时与持久层；CD 是客户应用职责（CLAUDE.md + ADR `202605041430` §3.1）|
| summary §5 | 性能 / SLO / 容量基准（如 p99 < 100ms） | 框架不知客户负载特征；SLO 在客户应用层定义，框架只提供接入点（F-09 引入 schema 字段，不预设具体值）|
| summary §5 | 微服务化拆分 / 服务网格集成 | 形态层冲突，N=每个客户不同部署形态 |
| summary §5 | journey 改 Gherkin | passCriteria + checkRef 比 Gherkin 更工程化（直接驱动 go test，不需 step definition 翻译层）|
| summary §5 | K8s CRD / etcd / informer / controller-runtime | K8s 是同范式参照而非同形态搬运 |
| summary §5 | 业务正确性审查 | accesscore/auditcore/configcore 是参考实现（K-04 ADR 决定其归属）|
| gap-assessment §7.4 | runtime topology API（实际请求 trace 拓扑） | 由 OTel + Jaeger/Tempo 生态承接 |
| gap-assessment §6 R-10 | examples 多 cell 协作样例 | `examples/ssobff` 已示范，触发条件 = 客户反馈"现有 ssobff 不足以演示 L2/L3 跨 cell 协作"|
| kernel-group1 G1-16/G1-17/G1-19/G1-20 | AfterStop 超时测试 / 并发 race detector / Level 注释 / Worker 命名 Run/Shutdown | P3 测试与命名调整，搭车 G-10 / G-01 同 PR 修；不单独立 PR |
| kernel-group2 G2-05/G2-15/G2-16/G2-17/G2-18/G2-20 | 命令终态写授权 / persistence 零测试 / inmem 并发 race / HandleResult 零值降级测试 / Redis 幂等 key 容量规划 / AdvanceCommand internal | P2/P3 加固，搭车 G-07 / G-08 / G-09 / R-01 同 PR；不单独立 PR |
| kernel-group3 G3-12/G3-14/G3-17/G3-21/G3-22/G3-23/G3-24/G3-25 | parser 无缓存 / CurrentKeyIDProvider 静默降级 / depgraph 互环测试 / clockmock 默认时间 / Catalog ListByStatus / nonce fake / metadata off-by-one / closure 无记忆化 | P2/P3 触发条件型；YAML 文件 < 100 / depgraph 互环 0 实例 / nonce fake 与 G-12 一起做；不单独立 PR |
| supporting-08 §3 cross-layer | `example-cells-isolation-ssobff` depguard 规则 | 触发条件：ssobff/ 出现 cells/ 子目录；当前无该子目录则规则缺失合理 |
| kernel-group3 G3-01 | YAML anchor bomb (HIGH 已降级 LOW) | yaml.v3 内置 `allowedAliasRatio()` Phase 2 已激活 + 1 MiB 文件大小限制 = 与 K8s CVE 修复等价；GoCell 是 CLI 工具非网络暴露 API server，可选加固（节点数 ≤10000）触发条件 = 出现 anchor bomb 实际报告 |
| 0504 综合 | 真冗余清零后所有"伪冗余" | software-review §2.2 已论证 6 项表面冗余实为分层意图（runtime 同名 alias / depgraph 双层 / pkg vs 顶层 contracts / pkg vs kernel ctxkeys / runtime/eventbus vs adapter / auth 三种 token），永不消除 |

---

## 5. 工时与排期

| 类别 | dev | review |
|---|---|---|
| Phase 0（K-01 + K-02 + K-03 + ADR 起草）| 24h | 12h |
| Phase 1（K-04 + K-05 ADR 落地 + F-01 + F-02）| 40h | 20h |
| Phase 2（K-06 + K-07 + K-08 + A-01..A-04）| 116h | 56h |
| Phase 3（R-01..R-03 + C-01..C-05 + G-01..G-04 + A-05..A-08 + Track G P2 子集）| 124h | 60h |
| Phase 4（G-05..G-15 + J-01..J-04 + F-03..F-10）| 142h | 70h |
| **合计** | **446h** | **218h** |

单线 ~16 周；K + G 双 lane 并行 ~9 周；K + G + F 三 lane 并行 ~7 周（F lane 异步穿插，不进 critical path）。

---

## 6. 与既有 029 roadmap 的关系

- **不依赖 029 在飞 PR**：本计划所有条目独立可起，但与 029 K#05 PR-V1-CODEGEN-MARKER-MIGRATE 在 cell.yaml 字段层有重叠 — 若 K#05 PR-A2/PR-B 先于本计划 K-06 ship，K-06 改造需 rebase 到 markergen 之上（工时不变，路径换）
- **与 029 K#04/K#06 codegen 协同**：本计划 K-08 ASSEMBLY-SCHEMA-SCAFFOLD 派生 `cmd/{id}/modules_gen.go` 应基于 K#04 framework，复用 `tools/codegen/` 的 render/writer/verify pipeline
- **与 029 R-04 R 路线图重叠**：029 文档未直接给 R-04 PR，本计划 R-01 EVENT-OBSERVABILITY-METRIC-PACK 是 R-04 的具体落地
- **不跟 029 Lane A/B/C/D 抢 reviewer**：建议本计划 Phase 0-1 与 029 K#05 PR-A2/PR-B 互斥时间窗，Phase 2 起可与 029 K Phase 4 装备类并行

---

## 7. 验收清单

- ✅ 16 份 0504 报告 finding 全部映射到 11 关键路径 + 21 并行轨道（合 32 PR）；P0 5 条全部进 Phase 0-1
- ✅ Top 8（summary §3）逐条覆盖：#1→K-02 / #2→K-06 / #3→K-05 / #4→A-01..A-04 / #5→R-01 / #6→K-08 / #7→K-07 / #8→K-02
- ✅ R-01..R-10 路线图条目：R-01→F-06 / R-02→K-05 / R-03→J-02 / R-04→R-01 / R-05→F-07 / R-06→F-08 / R-07→F-09 / R-08→F-01 / R-09→F-10 / R-10→Won't-do（触发条件型）
- ✅ Won't-do 区列出 7 大类边界 + 25 条 P2/P3 搭车项，避免单独 PR 噪声

---

## 参考

- **0504 review 报告原文**：`docs/reviews/20260504-*.md`（16 份）+ `docs/reviews/202605041800-systems-engineering-gap-assessment.md`
- **形态层口径**：`docs/architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md` §3
- **既有 roadmap**：`docs/plans/202605011500-029-master-roadmap.md`（本计划 §6 给出协同方式）
- **CLAUDE.md 与 rules**：`CLAUDE.md` + `.claude/rules/gocell/{api-versioning,error-handling,eventbus,observability}.md`
- **相关 ADR**：`docs/architecture/202605031600-adr-v1-schema-evolution.md` / `202605031900-adr-handler-vocabulary-collapse.md` / `202605021500-adr-kernel-clock-injection.md` / `202605040030-adr-wire-format-out-of-kernel.md`
