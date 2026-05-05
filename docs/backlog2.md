# GoCell Backlog2 — 四份归档审查的新增问题清单

> 日期：2026-04-29
> 基线：`origin/develop @ 4e2e00ad`（PR #331 已合）
> 输入：`bak/` 下 4 份团队归档审查
> - `20260426-develop-cross-layer-six-role-review/`（13 文件，跨层视角）
> - `20260426-layered-six-role-review/`（9 文件，分层视角 + master-summary）
> - `20260427-module-dataflow-six-role-review/`（12 文件，按 L2/L3 + 业务模块 + adapter 集成）
> - `20260427-per-relation-six-role-review/`（13 文件，逐对关系视角）
>
> 处理流程：4 个 reviewer 子 agent 并行通读 → 提取 finding → 对照 `develop @ 4e2e00ad` Grep/Read 验证 → 与 `docs/backlog.md` 交叉去重 → 合并视角重复项。
>
> **本文件只记录现有 `docs/backlog.md` 未覆盖的"新"开放问题**。已修复 / 已在 backlog 的项见 §10 索引段。
>
> 编号约定：`B2-{域}-NN`。{域} = K(kernel) / R(runtime) / C(cells) / A(adapters) / W(websocket) / X(cmd) / T(contracts)。

---

## §0 总览

| 项 | 数量 |
|---|---:|
| 4 份归档原始 finding（去重前） | ~84 |
| 已修复（最近 PR 关闭） | ~18 |
| 已在 `docs/backlog.md` | ~10 |
| 视角重复合并 | ~6 |
| **本文件登记新增项** | **70** |
| 其中 P0 | **6** |
| 其中 P1 | **40** |
| 其中 P2 | **24** |

**最高风险域（应优先评审）**：audit hash-chain 重启断链 / WebSocket 无认证授权 / RMQ 永久错误无限重连 / PG outbox claim 无 fencing / Redis Cluster 缺失 / setup admin 常驻 Public。

---

## §1 P0 高危项（6 条，发布前必做硬约束）

| # | 标题 | 文件:行 | 描述 | Cx | 来源 |
|---|---|---|---|---|---|
| ~~**B2-A-01**~~ | ~~POSTGRES-OUTBOX-CLAIM-FENCING-MISSING~~ | ✅ **closed PR#373 / fix/221-pg-outbox-fencing** — `lease_id UUID` 列 + 五个 CAS SQL 全部 `AND lease_id = $N`；ClaimedEntry.LeaseID 透传；archtest OUTBOX-LEASE-ID-CAS-01 静态守卫；ADR `docs/architecture/202605051600-adr-pg-outbox-fencing.md` | — | — |
| **B2-C-01** | **AUDITCORE-HASHCHAIN-RESTART-RECOVERY-MISSING** | `cells/auditcore/internal/domain/hashchain.go:31` + `cells/auditcore/cell.go` | `NewHashChain` 启动从空链开始，`initSlices` 未从 repo 恢复尾节点；多实例或重启后尾哈希不连续，链完整性无法验证；合规审计致命。**修复**：cell 启动时从 repo `SELECT last hash` 注入；考虑 leader 单写或全局 advisory lock。 | Cx4 | MD-02 |
| ~~**B2-W-01**~~ | ~~WEBSOCKET-UPGRADE-NO-AUTH-FAIL-OPEN~~ | ✅ **closed PR-V1-SEC-WS-AUTH-ACL** — UpgradeConfig.Authenticator 必填（nil → ErrWebsocketAuthenticatorMissing）；构造期 fail-fast；archtest SEC-07 静态守卫所有 UpgradeConfig 字面量必须声明 Authenticator | — | — |
| ~~**B2-W-02**~~ | ~~WEBSOCKET-BROADCAST-NO-ACL~~ | ✅ **closed PR-V1-SEC-WS-AUTH-ACL** — 删除无参 Broadcast；新增 BroadcastFilter（filter 必填，O(N)）+ BroadcastToSubject（O(1) subject index）；Hub 维护 subjectIdx；archtest SEC-08/09 静态守卫 | — | — |
| ~~**B2-A-02**~~ | ~~RMQ-RECONNECT-PERMANENT-ERROR-NO-TERMINAL~~ | ✅ **closed PR#379（029 A4 PR-V1-RMQ-TERMINAL）** — soft permanent classification 复用 `classifyDialError` 双层 sentinel（inferred N=2 确认 + definitive 协议级 + broker `Server=true && !Recover` + 字符串 fallback）；命中即 `markPermanent` 设 `permanentErr` 唤醒 WaitConnected，reconnect goroutine 持续 retry 不退；下次 dial 成功 → `markRecovered` 清 `permanentErr` → readyz 自愈。`sanitizeURL` fail-closed 丢 RawQuery+Fragment；删 hard-terminal 脚手架（StateTerminal phase + terminalCh + MaxReconnectAttempts + ErrAdapterAMQPReconnectExhausted）；ADR `docs/architecture/202605051700-adr-rmq-runtime-permanent-classification.md` | — | — |
| ~~**B2-A-03**~~ | ~~REDIS-CLUSTER-MODE-MISSING~~ | ✅ **closed PR#382（029 B10 PR-V1-REDIS-CLUSTER）** — `ModeCluster` + `Config.ClusterAddrs` + `buildClusterOptions`（URL/plain mix 检测、TLSConfig 抽取、per-URL 凭据合并冲突检测）；`validateConfig` fail-fast (a) ClusterAddrs 空 (b) DB!=0 (c) Addr 与 cluster mode 共存；`PoolSize` cluster 模式 5*GOMAXPROCS（vs standalone/sentinel 10*GOMAXPROCS）per-node sizing。`adapters/redis/idempotency.go` `leaseKey`/`doneKey` 包 `{key}:lease` / `{key}:done` hashtag — CRC16 只 hash 业务 key 让两 KEY colocate same slot 避 EVAL CROSSSLOT；6 处 Lua 脚本 doc 同步。`cmd/corebundle` 加 `GOCELL_REDIS_CLUSTER_ADDRS` env（与 `GOCELL_REDIS_ADDR` 互斥 + cluster 模式 DB=0 强制）。integration_cluster build tag + `GOCELL_TEST_REDIS_CLUSTER_ADDRS` 环境驱动；archtest `IDEMPOTENCY-LUA-HASHTAG-01`。**注**：B10 只解决 cluster slot；多租户 `KeyNamespace` cell prefix（`B2-A-27`）仍归 029 B11 待办。 | — | — |
| **B2-C-02** | **SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT** | `cells/accesscore/cell_routes.go:73` + `slices/setup/handler.go:46-58` + `contracts/http/auth/setup/admin/v1/contract.yaml:5` | setup 端点常驻 `Public: true`；contract 标 `lifecycle: active`；410 Gone 仅在 admin 已存在时返回，未初始化窗口仍可被匿名首管抢注；多视角共识根因。**修复**：移到 `/internal/v1/setup/`（service-token only）+ contract `lifecycle: bootstrap`，或一次性 bootstrap token（env 注入消费）。`A26-R2` 限速只是缓解。 | Cx3 | CL-06 / LY-06 / PR-01 / PR-02 / PR-07 |

---

## §2 kernel / 治理 / 错误语义（8 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| ~~**B2-K-01**~~ | ~~WRAPPER-ERROR-REDACTOR-DEFAULT-IDENTITY~~ | ✅ **closed PR#366（029 A3 PR-V1-SEC-WRAPPER-REDACTOR）** | `kernel/wrapper/consumer.go` + `kernel/wrapper/lifecycle.go` + `pkg/redaction/` | 默认 redactor 改 fail-closed 硬编 `pkg/redaction.RedactError`；删整条 opt-out wiring（`ErrorRedactor` type / `WithConsumerErrorRedactor` / `bootstrap.WithErrorRedactor` / `middleware.WithErrorRedactor` / `Bootstrap.errorRedactor` / `ContractTracingMiddleware` 第二参）— 比"最小脱敏"修复方向更激进，对齐 Vault `log_raw=false` + Go stdlib `URL.Redacted()` 哲学；`runtime/outbox.SanitizeError` 收口到 `pkg/redaction` 单源；新增 `tools/archtest/span_record_error_redact_test.go` 守护每个 `span.RecordError(...)` inline `redaction.RedactError(...)` | — |
| **B2-K-02** | KERNEL-ERROR-FIRST-PANIC-RESIDUAL | P1 | `kernel/wrapper/handler.go:56` + `kernel/cell/auth_plan.go:107` + `pkg/contracttest/` | `MustNewAuthJWT` 等 Must 系列与 error-first 构造器混用；composition root 残留 panic。**修复**：审计所有 Must*，把生产路径改 error-first；保留 Must 仅作为 test-only / cmd 顶层 wrapper。 | Cx3 |
| **B2-K-03** | ASSEMBLY-REF-INTERFACE-IMPLICIT-CAST | P1 | `kernel/cell/auth_plan.go:175-181` + `runtime/bootstrap/auth_plan_apply.go:175` | `asmCellLookup` 通过 `asm.(assemblyWithCell)` 类型断言获取 Cell；kernel 与 runtime 间隐式契约，新 assembly 实现易漏配 method 静默失败。**修复**：把 `assemblyWithCell` 上提为 kernel 显式接口（`kernel/cell.AssemblyRef.Cell(id) Cell`）。 | Cx3 |
| ~~**B2-K-04**~~ | ~~GOVERNANCE-CH-04-ERRCODE-MIRROR-DRIFT~~ | ✅ **closed PR#368** | `kernel/governance/rules_http_response_alignment.go` | ~~`errcodeNameToStatus` 70+ 行手抄 `pkg/errcode/status.go` 状态码映射~~；PR#368 删除手抄表，换 `errcodeKindNameToStatus` 由 `errcode.Kind.Status()` 单源派生（reflect-built ~10 行），CH-04 不再可能与 errcode 漂移 | — |
| **B2-K-05** | METADATA-PARSER-ERROR-PATH-LEAK | P2 | `kernel/metadata/parser.go:190,202` | parse error 公共消息直接含 fs 内部路径；低强度信息泄露 + 路径暴露 CI runner 结构。**修复**：error 双通道：public 仅含 cell/slice ID + 字段路径，internal slog 保留 fs path。 | Cx2 |
| **B2-K-06** | EVENTROUTER-CONSUMERGROUP-CELLID-CONFUSION | P2 | `runtime/eventrouter/router.go:364` | `Subscription.CellID = h.consumerGroup`；语义混淆，cell 切片与消费组名通过同一字段表达，下游 metrics label / 日志属性自相矛盾。**修复**：Subscription struct 显式拆分 `CellID` 与 `ConsumerGroup` 两字段。 | Cx3 |
| **B2-K-07** | CONTRACTTEST-UNDECLARED-REF-NO-OP | P1 | `pkg/contracttest/contracttest.go:170,189` | 测试调 `MustValidateRequest("not-declared-key", ...)` 时静默 `return`；key 写错时测试假通过。**修复**：未声明 key 改 `t.Fatalf`；保留显式 opt-in opt-out 时也只允许 explicit allowlist。 | Cx1 |
| **B2-K-08** | ASSEMBLY-RACE-TEST-COGNITIVE-COMPLEXITY | P2 | `kernel/assembly/snapshots_race_test.go` (PR#370 引入；SonarCloud `brain-overload` L141 报 32 / 阈值 15) | `TestAssembly_StartConcurrentSnapshots_RaceDetector` (line 36-120) 单函数把 fixture 注册 + Phase 1 阻塞 cell + Start goroutine + N reader goroutines + ready barrier + cleanup 全堆在一处；race window 设定（pre-register 3 fast cell + last-cell init gate + reader ready chan）逻辑可读但深嵌套 select / if / 闭包共同推高 cognitive complexity。SonarCloud `brain-overload` 阈值 15，实测 32（手算 ~12-14，差异可能来自 Sonar 把 goroutine 闭包 + select case 多倍累计）。PR#370 review 阶段未拦下；PR#378 (G-03) 全量扫描重新暴露。**修复**：拆分为 `setupRaceFixture(t) (*Assembly, chan struct{}, *sync.WaitGroup)` + `spawnSnapshotsReaders(...)` + `awaitReadersReady(...)` 三个 helper；主 test 只剩 register / start / unblock / assert 四步。注意：拆分后必须保留 race window 的确定性（reader ready barrier 不可改成 timing-based sleep）；`go test -race -count=20 ./kernel/assembly/...` 必须保持稳定。 | Cx2 |

---

## §3 runtime / bootstrap / health / observability（9 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-R-01** | HEALTH-LISTENER-FALLBACK-NO-STRICT-FAIL-FAST | P2 | `runtime/bootstrap/bootstrap_phases.go:583-596` | HealthListener 缺失时静默回退 PrimaryListener；探针/业务入口隔离弱化；strict 模式 fail-fast 缺失。**修复**：bootstrap 加 `WithStrictHealthListener()`，缺失时 fail；至少 slog.Warn 升 Error 并暴露 audit attribute。 | Cx2 |
| **B2-R-02** | CELLS-READYZ-MISSING-REPO-PROBE | P1 | `cells/configcore/cell.go:204` + `cells/auditcore/cell.go:191` | configcore / auditcore 的 `HealthCheckers()` 仅接 outbox emitter；底层 repo（PG / 内存）健康未纳入；DB 故障下 readyz 仍 OK。**修复**：cell 注入 repo `Pingable` 接口，HealthCheckers 聚合 emitter + repo + downstream 三档。 | Cx2 |
| **B2-R-03** | BOOTSTRAP-ROLLBACK-ERROR-NOT-PROPAGATED | P1 | `runtime/bootstrap/run_state.go:113,121` | rollback 步骤失败仅 `slog.Warn` 后 `return cause`（原启动错误），rollback 错误未并入返回值；调用方无从感知"启动失败 + 资源未完全清理"双重故障。**修复**：用 `errors.Join(cause, rollbackErr)` 返回；或返回结构化 error 含两个 chain。 | Cx1 |
| ~~**B2-R-04**~~ | ~~ERRCODE-4XX-CLASSIFY-DUAL-SOURCE-DRIFT~~ | ✅ **closed PR#368** | `pkg/errcode/classify.go` + `pkg/errcode/errcode.go` | ~~`expected4xxCodes` 白名单与 `WriteDomainError` 状态码映射分散维护~~；PR#368 (refactor/514-pkg-breaking-cleanup) 删除 `expected4xxCodes` whitelist，`IsExpected4xx` 改 `ec.Kind.IsClient()` 单源；Kind 同时派生 HTTP status 与 4xx 分类 | — |
| **B2-R-05** | OTEL-METRIC-PROVIDER-CTX-BACKGROUND | P1 | `adapters/otel/metric_provider.go:174,178,185` | metric record 固定使用 `context.Background()`；trace-metric exemplar 关联在 adapter 层断裂；分布式 trace 关联 metric 失败。**修复**：metric Record 接受 caller ctx，构造期注入 propagator。 | Cx4（评估范围） |
| **B2-R-06** | OTEL-TRACER-PROVIDER-NOT-GLOBAL | P1 | `adapters/otel/tracer.go:56,73` | `NewTracer` 只创建局部 TracerProvider，未注册到 `otel.SetTracerProvider`；三方依赖 `otel.GetTracerProvider()` 的 instrumentation 静默落到 noop。**修复**：composition root 调 `otel.SetTracerProvider`；或文档化"必须注入根"约束 + archtest 锁定。 | Cx2 |
| **B2-R-07** | OTEL-TRACER-SHUTDOWN-NO-DEADLINE | P1 | `adapters/otel/tracer.go:63,65` | shutdown 完全依赖外部 ctx；调用方传 `context.Background()` 时停机 flush 不可控；Pod 终止时可能挂起到 SIGKILL。**修复**：shutdown 内部派生 timeout（默认 5s），与外部 ctx Min。 | Cx1 |
| **B2-R-08** | OTEL-OBSERVABLE-CALLBACK-MANUAL-UNREGISTER | P1 | `adapters/otel/pool_collector.go:43,110` | `RegisterCallback` 返回 unregister fn 需调用方手工管理；未释放则 callback 持续触发；MeterProvider 重建时残留。**修复**：构造期 `WithLifecycle` 接 cell.Lifecycle，OnStop 自动 unregister；或返回 `io.Closer` 强制接入 cleanup。 | Cx3 |
| **B2-R-09** | OTEL-ATTR-CACHE-KEY-COLLISION-UNBOUNDED | P1 | `adapters/otel/metric_provider.go:84,96,101` | attrCache key 用字符串拼接（`"key1=val1,key2=val2"`），未做转义，且无上界；高基数 label / 注入字符均可造成 key 碰撞或内存膨胀。**修复**：key 用 sha256 hex / FNV 或 sorted (k,v) tuple；加 LRU max size + drop。 | Cx3 |
| ~~**B2-R-10**~~ | ~~BOOTSTRAP-TEST-LISTENER-NO-CLEANUP~~ | ✅ **closed PR#373** | `runtime/bootstrap/bootstrap_test.go:78` | ~~`newLocalListener` 创建 `net.Listener` 但不注册 `t.Cleanup`~~；PR#373 在 `newLocalListener` 内注入 `t.Cleanup(func(){_=ln.Close()})`，闭包持引用作 GC keep-alive，确保 `holdLn`/`collideLn` 等"占位端口"模式在测试运行期间端口不被提前释放。本地 `-race -count=20` 验证 `TestPhase7BindListeners_OwnedSocket_ClosedOnSiblingFailure` + `TestDualListener_BootstrapOwnedPrimary_InternalBindFails` 全绿。 | Cx1 → 完成 | discovered via /fix PR #373 |
| **B2-R-11** | BOOTSTRAP-DUAL-LISTENER-CLOSE-RACE-CI-FLAKE | P3 | `runtime/bootstrap/dual_listener_test.go:891` | `TestPhase7ServeAll_DualListener_NoCloseRace` 在 race build 下 GitHub Actions runner 偶发 `teardown[teardown_http_drain]: listener "primary" shutdown: context deadline exceeded`（PR#380 race job run 25356757630）。测试逻辑：4 个并发 HTTP `GET /api/v1/test/ping` 期间 cancel ctx，等 `done` 或 `testtime.SelectShutdown` 超时；race build 下 graceful shutdown 偶发突破 SelectShutdown 预算。**本地不可复现**：`-race -count=20` 单测 + 包级 1× 全绿；develop 同测试最近 race detector run（`8c7e7ebb` / `ef3edc6b` / `8af79a9a`）全 success。**修复方向**：(a) `testtime.SelectShutdown` 在 race build 下放宽（runtime/race build tag 派生）；(b) 审计 `Bootstrap.teardown_http_drain` 内部 listener.Shutdown ctx 派生，避免 race build 下 4 并发 + 1 cancel 的最坏路径超过 SelectShutdown；(c) 暂列 known-flake，CI rerun 即可。 | Cx2 | discovered via PR #380 race job |

---

## §4 cells / 业务收尾（12 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-C-03** | CELL-INIT-INFRA-TYPE-LEAK | P1 | `cells/configcore/cell_init.go:34` + `cells/configcore/cell.go:157` | Cell.Init 仍直接构建 slice + 接收 PG pool 等 adapter 类型；Cell 边界泄漏基础设施类型；composition root 与 Cell 装配混杂。**修复**：cell 暴露 ports（接口），slice 装配下沉到 corebundle composition root；cell.Init 只接受 ports.* 接口。 | Cx3 |
| **B2-C-04** | AUDITAPPEND-L2-FAILURE-INJECTION-TEST-MISSING | P1 | `cells/auditcore/slices/auditappend/outbox_test.go:50` | 仅有 stub 级测试，无 PG-level 失败注入（DB 写成功 + outbox 失败 / DB 失败 + outbox 已写）；L2 原子失败语义未实证。**修复**：用 testcontainers PG + faulty hook（`pgmock` / sqlhooks），三个失败矩阵覆盖 + assert outbox 状态/事件未发。 | Cx2 |
| **B2-C-05** | AUDITAPPEND-ACTOR-MISSING-FALLBACK-SYSTEM | P1 | `cells/auditcore/slices/auditappend/service.go:133` | actor 缺失时降级为 `system` 仍 Ack；可追责性削弱；恶意攻击者借此清空 actor 字段。**修复**：缺 actor 改 `DispositionReject + PermanentError`，事件入 DLX；或 actor schema validation 在 contract 层 required（已 strict）+ 服务层 fail-closed。 | Cx2 |
| **B2-C-06** | SESSIONLOGOUT-CONSUMER-NO-ACTION-VALIDATE | P1 | `cells/accesscore/slices/sessionlogout/consumer.go:69` | role-change consumer 只校验 `userId` 非空，不校验 `action` 是否为合法枚举（`assigned`/`revoked`）；任意/未知 action 均触发 `RevokeByUserID`，可被滥用扩大会话回收范围。**修复**：白名单 action 枚举，未知值 PermanentError → DLX。 | Cx2 |
| **B2-C-07** | CONFIGCLIENT-STATUS-DISPOSITION-MISMAP | P1 | `cells/accesscore/internal/adapters/http/configclient.go:91` + `cells/accesscore/slices/configreceive/service.go` | configclient HTTP 请求 401/403 等不可恢复错误仍走 Requeue 重试，产生持续 noise。**修复**：状态码分级 → Disposition 映射（401/403/410 → Reject Permanent；404 已特判；429/5xx → Requeue）。 | Cx2 |
| **B2-C-08** | CONFIGCORE-EVENT-DECODER-STRICT-UNKNOWN-FIELDS | P1 | `cells/configcore/internal/events/config_events.go:82` | event decode 用 `json.NewDecoder + DisallowUnknownFields`；与 contract v1 演进策略（响应可加字段）冲突；上游 producer 加 optional 字段后，下游 consumer 全量拒收。**修复**：unknown field 默认接受（lenient），strict 改为可选 dev-only flag；或与 PR-CI-3 V1-RESPONSE-EVOLVE 协调统一。 | Cx2 |
| **B2-C-09** | AUDITQUERY-RAW-PAYLOAD-EXPOSURE | P1 | `cells/auditcore/slices/auditquery/handler.go:35,42` | `payload` 直接回传原始事件内容；可能含敏感字段（PII / 密码 / token）；audit query 鉴权后仍泄漏。**修复**：handler 层加 redaction policy（按 contract.yaml `sensitive` 字段标记或全局 deny-list）；查询响应字段可配。 | Cx2 |
| **B2-C-10** | AUDITAPPEND-GLOBAL-MUTEX-13-TOPIC-SERIAL | P1 | `cells/auditcore/slices/auditappend/service.go:93,165` | `s.mu.Lock()` 在 `HandleEvent` 入口；13 个 topic consumer 全局串行化；吞吐受限，单慢消息阻塞所有。**修复**：锁粒度降到 hash-chain 链头；或改用 single-writer goroutine + chan 模式（hash-chain 顺序仍单线程，但其他工作并行）。 | Cx3 |
| **B2-C-11** | CONFIGSUBSCRIBE-TOMBSTONE-UNBOUNDED | P2 | `cells/configcore/slices/configsubscribe/service.go:29,169` | tombstone 常驻内存无 TTL / 容量上限；长期高 churn 键空间持续增长；OOM 风险。代码注释已承认 out-of-scope。**修复**：加 TTL（默认 24h）+ max entries（默认 10k） + LRU 淘汰。 | Cx2 |
| **B2-C-12** | AUDIT-HMAC-KEY-MIN-LENGTH-MISSING | P2 | `cells/auditcore/cell.go:319` | HMAC key 仅校验非空，未做最小长度 / 强度门禁；32 字节以下 key 抗碰撞强度不足。**修复**：构造期校验 `len(key) >= 32`；HKDF 派生时 input 长度校验。 | Cx1 |
| **B2-C-13** | L2-CROSS-LAYER-E2E-REGRESSION-MISSING | P2 | `cells/accesscore/slices/setup/service_test.go` + `tests/integration/` | L2 跨层端到端回归不足：事务写入 + outbox insert + relay publish 联动场景仅 stub 验证；真实 PG + relay + broker 链路无全量覆盖。**修复**：与 `TEST-JOURNEY-ROOT-HARNESS-01` 协调，建 `tests/integration/l2_atomicity/` harness，覆盖 setup/identity/role-assign 三主链。 | Cx3 |
| **B2-C-14** | HASHCHAIN-CROSS-RESTART-CONTINUITY-TEST-MISSING | P2 | `cells/auditcore/slices/auditappend/service_test.go:110` | 缺跨重启 / 跨实例 hash-chain 连续性回归；本质同 B2-C-01，但额外补 test。**修复**：B2-C-01 实现后追加 testcontainers PG + restart 注入测试。 | Cx2 |

---

## §5 adapters 加固（21 条，按子模块）

### 5.1 PostgreSQL

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| ~~**B2-A-04**~~ | ~~PG-OBSERVABILITY-INJECT-AFTER-VALIDATE~~ | ✅ closed fix/221-pg-outbox-fencing — outbox_writer.go 把 InjectObservabilityFromContext 移到 Validate 之前；TestOutboxWriter_Write_ObservabilityInjectedBeforeValidate 锁回归 | — | — |
| ~~**B2-A-05**~~ | ~~PG-RELAY-FAIL-WRITE-UNHANDLED-ROWS~~ | ✅ closed fix/221-pg-outbox-fencing — handleFailedEntry 观察 MarkRetry/MarkDead 的 updated bool；stale-lease 路径 slog.Warn + 不增 stats；archtest OUTBOX-MARK-RETURNS-BOOL-01 + 2 个 white-box 单测锁回归 | — | — |
| ~~**B2-A-06**~~ | ~~PG-RECLAIM-STALE-NO-LIMIT-LONG-TX~~ | ✅ closed fix/221-pg-outbox-fencing — reclaimStaleQuery 加 LIMIT 1000（reclaimBatchSize 包级常量）；relay 调度循环自然消化残余；TestPGOutboxStore_ReclaimStale_RespectsBatchLimit 集成回归 | — | — |
| ~~**B2-A-07**~~ | ~~PG-OUTBOX-METADATA-NO-BYTE-LIMIT~~ | ✅ closed fix/221-pg-outbox-fencing — outbox_writer.go MaxMetadataBytes=64<<10 包级常量；Write/writeBatchChunk marshal 后 size check；archtest OUTBOX-METADATA-MAX-BYTES-01 + TestOutboxWriter_Write_MetadataExceedsLimit 锁回归 | — | — |
| **B2-A-08** | PG-REFRESH-STORE-AMBIENT-AND-STANDALONE-TX-MIXED | P1 | `adapters/postgres/refresh_store.go:141,190,227` | 同一接口混合 ambient tx（依赖 `RunInTx`）和独立提交路径；事务契约对调用方不可见；潜在双写风险。**修复**：拆两接口（`RotateInTx` / `RotateStandalone`），或文档化所有方法均要求 ambient tx，archtest 锁定。 | Cx3 |
| **B2-A-09** | PG-REFRESH-REJECT-TIMING-SIDECHANNEL | P1 | `adapters/postgres/refresh_store.go:221,295,330` | 不同拒绝路径耗时 / 日志特征不一致；可被时序攻击区分"token 不存在"vs"token 已用过"。**修复**：所有拒绝路径走同一 fixed-time 比较 + 同一 slog 字段；增加 timing test。 | Cx3 |
| **B2-A-10** | PG-READYZ-NO-SCHEMA-COMPATIBILITY | P1 | `adapters/postgres/pool_resource.go:69` | `Checkers()` 调用 `pool.Health`（Ping）；schema_guard 检测到 invalid index 时未并入 readyz；migration 不兼容时 readyz 仍 OK。**修复**：`Checkers()` 聚合 Ping + schema_guard 结果；schema 不一致返回 503。 | Cx3 |
| **B2-A-11** | PG-CONSTRUCTOR-ERROR-MODEL-MIXED | P1 | `adapters/postgres/refresh_store.go:114` 等 | postgres 包对外暴露混杂 error / panic / nil deref / `MustNew` / 多 DB handle 并存；调用方判断成本高。**修复**：审计 New*/MustNew*；New* 全 error-first；MustNew 仅作 cmd 顶层 wrapper；archtest 锁定。 | Cx3 |
| ~~**B2-A-12**~~ | ~~PG-SCHEMA-GUARD-QUALIFIED-NAME-DRIFT~~ | ✅ closed fix/221-pg-outbox-fencing — schema_guard SQL JOIN pg_namespace 双侧（index + table），SELECT 拼 `n.nspname \|\| '.' \|\| c.relname`；TestDetectInvalidIndexes_WithInjectedInvalid 集成断言 `public.idx_outbox_pending_v2` 形态 | — | — |
| **B2-A-13** | PG-POOL-TX-ROLLBACK-LOG-LEAKS-DRIVER-ERROR | P2 | `adapters/postgres/pool.go:87,113` | 基础设施日志透出原始 driver error；脱敏边界与 errcode 公共消息不一致；可能泄露 query / param 片段。**修复**：rollback 日志只记 wrapped errcode + slog `internal_error` attribute；driver error 仅放 debug 级。 | Cx2 |

### 5.2 RabbitMQ

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-A-14** | RMQ-STOP-INTAKE-PREFETCH-NOT-DRAINED | P1 | `adapters/rabbitmq/subscriber.go:914` | StopIntake 与 runCtx cancel 耦合；prefetch 缓冲消息可能未排空就 cancel；丢消息或重投。**修复**：StopIntake 实现"先停拉新 + 排空 prefetch + 等 inflight ack" 三段；超时 fail。**归宿**：029 roadmap **B12 PR-V1-RMQ-LIFECYCLE-HARDEN**。 | Cx3 |
| **B2-A-15** | RMQ-CHANNEL-COUNT-NO-UPPER-BOUND | P1 | `adapters/rabbitmq/connection.go:171` | 每个 publisher / subscriber 创建 channel 无上限；可触发 broker `channel_max`；连接死锁。**修复**：connection 加 `MaxChannelsPerConn`（默认 256）+ pool 复用；超限 fail-fast。**归宿**：029 roadmap **B12 PR-V1-RMQ-LIFECYCLE-HARDEN**。 | Cx3 |
| **B2-A-16** | RMQ-PUBLISH-NACK-CONFIRM-TIMEOUT-NO-WARN | P1 | `adapters/rabbitmq/publisher.go:133,136,143` | NACK / confirm timeout 错误路径无 slog.Warn；静默丢失，下游基于"成功发布"假设导致状态分叉。**修复**：所有错误返回路径统一 slog.Warn + emit `publish_failed` metric；与 outbox relay 协调失败语义。**归宿**：029 roadmap **B12 PR-V1-RMQ-LIFECYCLE-HARDEN**。 | Cx1 |
| **B2-A-17** | RMQ-CONFORMANCE-EVENTBUS-SEMANTIC-TEST-MISSING | P1 | `adapters/rabbitmq/conformance_test.go:18` | 缺 EventBus 真实语义集成测试（PermanentError → Reject DLX / Receipt commit / Disposition 三分支）。**修复**：testcontainers RMQ + 三 fixture：handler 返回 Ack / Requeue / Reject Permanent，断言 broker 端结果（DLX 入队 / 重新投递 / consumer ack 计数）。**归宿**：029 roadmap **B13 PR-V1-RMQ-CONFORMANCE-AND-CLOSURE**（同时吸收 PR#379 review FU 两项）。 | Cx3 |
| **B2-A-18** | ADAPTER-CONNECT-TIMEOUT-INCONSISTENT | P1 | `adapters/rabbitmq/connection.go:273` + `adapters/postgres/pool.go:69` | RMQ DefaultDial 直接调 `amqp.Dial` 无内置超时；PG pool 仅 ctx.Ping，无 adapter 级连接预算。**修复**：每个 adapter 暴露 `WithConnectTimeout(d)`，缺省 5s；构造期 fail-fast。**归宿（拆双）**：RMQ 半 → 029 roadmap **B13 PR-V1-RMQ-CONFORMANCE-AND-CLOSURE**；PG 半 → 029 roadmap **B8 PR-V1-PG-CONNECT-STRICT**（B8 范围已收窄为纯 PG）。 | Cx2 |
| **B2-A-29** | RMQ-PR379-FU-DOC-DRIFT | P2 | `docs/guides/adapter-config-reference.md:105` + `adapters/rabbitmq/subscriber.go:95-119` | PR#379 (RMQ-TERMINAL) review 二轮 FU#1：SubscriberConfig 表写了不存在字段 `ShutdownTimeout`（已删，仅测试注释残留），漏 `StopIntakePerCallTimeout`（默认 2s）和必填 `Clock`（NewSubscriber 对 nil fail-fast）；调用方按文档接入会编译失败或启动崩。**修复**：重写 SubscriberConfig 表对齐真实 struct + 验证 Health/WaitConnected godoc 与 permanent classifier 单源已对齐。**归宿**：029 roadmap **B13 PR-V1-RMQ-CONFORMANCE-AND-CLOSURE**。**Source**：PR#379 review 二轮（2026-05-06）。 | Cx1 |
| **B2-A-30** | RMQ-SUBSCRIBER-TERMINAL-PROPAGATION-TEST | P2 | `adapters/rabbitmq/subscriber.go:374-378` `awaitReconnect` | PR#379 (RMQ-TERMINAL) review 二轮 FU#2：subscriber 层 terminal 传播无回归。`awaitReconnect → WaitConnected → isTerminalConnectionError → return waitErr` 决定 EventRouter/Bootstrap 能否收到 `ErrAdapterAMQPConnectPermanent`，但只在 connection 层（`connection_runtime_terminal_test.go`）有测试；以后 awaitReconnect 判断被改坏，CI 不会拦下。**修复**：新增 `subscriber_terminal_propagation_test.go` — mock connection markPermanent → WaitConnected 返回 permanentErr → 断言 `Subscriber.Subscribe` 返回 ErrAdapterAMQPConnectPermanent（vs 当前测试只覆盖 clean ctx cancel 路径）。**归宿**：029 roadmap **B13 PR-V1-RMQ-CONFORMANCE-AND-CLOSURE**。**Source**：PR#379 review 二轮（2026-05-06）。 | Cx2 |

### 5.3 OTel / Prometheus / Redis / S3

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-A-19** | OTEL-SPAN-SETATTR-INSECURE-PLAINTEXT | P1 | `adapters/otel/span.go:43,51` | `SetAttributes` 对 string 直接透传；`Insecure=true` 时明文出站 collector；敏感字段（token / PII）泄露到 OTel backend。**修复**：构造期校验 `Insecure=false` 或显式 `WithUnsafePlaintext()`；attribute 值长度上限 + redaction hook。 | Cx2 |
| **B2-A-20** | OTEL-SIMPLE-TRACER-PROPAGATION-ASYMMETRY | P2 | `runtime/observability/tracing/tracer.go:77` | `simpleTracer` 与 OTel Tracer 共享接口但 propagator 语义不对称；从 simpleTracer 切到 OTel 时跨进程链接静默丢失（traceparent 不注入）。**修复**：简化 simpleTracer 仅 noop 或要求 caller 显式 propagator；archtest 锁定不可在生产路径用 simpleTracer。 | Cx2 |
| **B2-A-21** | OTEL-MESSAGING-COLLECTOR-FORMAT-PCT-LITERAL | P2 | `adapters/otel/messaging_channel_collector.go:65` | error 文案把 `%d` 当字面量输出（缺 `fmt.Errorf` / `Sprintf` 包装）；线上日志可读性差。**修复**：改 `fmt.Errorf("...: %d", n)`。 | Cx1 |
| **B2-A-22** | PROMETHEUS-METRICS-HANDLER-NO-TIMEOUT | P1 | `cmd/corebundle/metrics.go:83` | `promhttp.HandlerOpts{}` 零值无 Timeout；Registry.Gather 大表（如 hookobserver per-cell label）可阻塞 HTTP；scraping 慢拉爆 worker。**修复**：`HandlerOpts{Timeout: 5*time.Second}`；与 Prom scrape interval 协调。 | Cx1 |
| **B2-A-23** | PROMETHEUS-HOOKOBSERVER-CELLID-LABEL-NO-VALIDATE | P1 | `adapters/prometheus/hook_observer.go:114-117` | CellID 标签值无格式校验；恶意 / 错误 cell.yaml 可注入 `\n` / 空格污染指标输出；Prom parser 报错或 dashboard 错位。**修复**：构造期校验 `cellID` 匹配 `^[a-z][a-z0-9-]*$`，违例 fail-fast。 | Cx1 |
| **B2-A-24** | PROMETHEUS-RACE-TEST-MISSING | P1 | `adapters/prometheus/metric_provider_test.go` | CounterVec / HistogramVec 并发 Lock/Unlock 未加 `-race` 测试；潜在 goroutine 安全问题。**修复**：添加 `t.Parallel()` + 多 goroutine 并发 record fixture，CI 跑 `-race`。 | Cx2 |
| **B2-A-25** | PROMETHEUS-LOOKUP-VEC-LABELS-DUPLICATE | P2 | `adapters/prometheus/metric_provider.go:201-227` | `lookupCounterVecLabels` / `lookupHistogramVecLabels` 99% 重复；维护成本与漂移风险。**修复**：泛型抽 `lookupVecLabels[T metric.Vec](...)` 共用，或下沉到 helper。 | Cx2 |
| **B2-A-26** | REDIS-RECEIPT-COMMIT-RELEASE-RACE | P1 | `adapters/redis/idempotency.go:136-200` | Commit/Release 并发情况下幂等保证可破坏（Commit 完成同时另线程 Release 当前 receipt）。**修复**：Commit/Release 内部用 `Lua` 脚本原子 CAS（current_owner==caller && state==committing）。 | Cx3 |
| **B2-A-27** | REDIS-MULTI-TENANT-KEY-COLLISION | P1 | `adapters/redis/idempotency.go:127-130` | 不同 cell 间 Idempotency / Nonce / Cache key 共享前缀；多租户场景碰撞或越界访问。**修复**：构造期注入 `KeyNamespace`（cell ID），所有 key 自动 prefix；archtest 守护 cell 级隔离。 | Cx3 |
| **B2-A-28** | REDIS-PASSWORD-OPTIONAL-FAIL-OPEN | P1 | `adapters/redis/client.go:62-68` | `Password` 可选；未设时连接无认证 Redis 也通过；生产配置漏 password 时 fail-open。**修复**：real mode 下 `Password` 必填（与 TLS 一致 fail-closed）；dev 显式 `WithUnsafeNoPassword()`。 | Cx2 |
| **B2-A-29** | REDIS-RACE-TEST-MISSING | P1 | `adapters/redis/distlock_test.go` | DistLock / Cache / Nonce / Idempotency 并发竞争未在 `-race` 下覆盖。**修复**：补 testcontainers Redis + `t.Parallel()` 多 goroutine fixture。 | Cx3 |
| **B2-A-30** | REDIS-DISTLOCK-RENEW-TTL-PRECISION-LOSS | P2 | `adapters/redis/distlock.go:50-56` | TTL Seconds 转换截断；锁过期可能比预期早若干毫秒。**修复**：用 `PEXPIRE` (ms 精度) 替代 `EXPIRE`；或 ttl < 1s 时 fail-fast。 | Cx2 |
| **B2-A-31** | REDIS-SENTINEL-TLS-INCOMPLETE | P2 | `adapters/redis/client.go:200-215` | Sentinel 协议下 TLS 配置未完整透传；连接 Sentinel 无 TLS。**修复**：`SentinelOptions.TLSConfig` 同步注入。 | Cx2 |
| **B2-A-32** | S3-INTEGRATION-TEST-MISSING | P1 | `adapters/s3/s3_test.go:11` | 仅有 Config.Validate 单测，无 MinIO testcontainers 集成测试；put/get/list 真实路径未覆盖。**修复**：testcontainers MinIO + 完整 RW + 错误注入（403/超时/分片）。 | Cx2 |
| **B2-A-33** | REDIS-SENTINEL-AND-LOGVALUE-BASELINE-GAPS | P2 | `cmd/corebundle/redis.go:18-22` + `adapters/redis/client.go:90-104` | B10 ship 时显式范围外的两个 baseline 缺口：(a) `cmd/corebundle/redis.go` 仅识别 `GOCELL_REDIS_ADDR` / `GOCELL_REDIS_CLUSTER_ADDRS`；sentinel 模式（`SentinelAddrs` / `SentinelMaster`）从未接 env，sentinel 部署在生产 main 程序里实际不可用；(b) `Config.LogValue` 在 standalone/sentinel 模式下输出 `addr`，sentinel 下该字段为空字符串，运维 slog 误导。**修复**：加 `GOCELL_REDIS_SENTINEL_ADDRS` + `GOCELL_REDIS_SENTINEL_MASTER` env；`LogValue` 按 Mode 切字段集（standalone=addr/db、sentinel=sentinel_addrs/sentinel_master/db、cluster 已对称）。来源：B10 PR-V1-REDIS-CLUSTER 范围切割。 | Cx2 |
| **B2-A-34** | REDIS-CLUSTER-CI-LIVE-GATE-MISSING | P2 | `.github/workflows/_build-lint.yml` Redis cluster compile gate step + `adapters/redis/cluster_real_test.go` | B10 PR-V1-REDIS-CLUSTER 只在 CI 加了 `go vet -tags=integration_cluster` **编译 gate**，**不是 live 测试 gate**：cluster_real_test.go 的真集群路径（CROSSSLOT / IdempotencyClaimer multi-KEY EVAL / NewClient health 探活）只在本地用 `make test-integration-cluster` + `GOCELL_TEST_REDIS_CLUSTER_ADDRS` 指向预启动 cluster 时才跑；PR gate / nightly 都跳过。后果：go-redis cluster 行为变更、Redis server 升级、我们 cluster 路径回归只能靠 staging 兜底。**修复**：在 GitHub Actions Linux runner 上跑 grokzen/redis-cluster service container（host-network，6 节点 7000-7005），独立 workflow 或 nightly job；预算约 +3-5min CI 时间 + 镜像拉取 ~50MB。本地 `make test-integration-cluster` 已就绪，不阻塞迁移。来源：B10 PR-V1-REDIS-CLUSTER 范围切割。 | Cx3 |

---

## §6 WebSocket（独立模块，6 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| ~~**B2-W-01**~~ | ~~(P0) UPGRADE-NO-AUTH~~ | — | ✅ closed PR-V1-SEC-WS-AUTH-ACL — 见 §1 B2-W-01 | — | — |
| ~~**B2-W-02**~~ | ~~(P0) BROADCAST-NO-ACL~~ | — | ✅ closed PR-V1-SEC-WS-AUTH-ACL — 见 §1 B2-W-02 | — | — |
| **B2-W-03** | WS-OBSERVABILITY-MISSING | P1 | `runtime/websocket/hub.go` | 无 Prometheus 指标 / 连接生命周期追踪；连接数 / 消息率 / 错误 / 慢 client 全黑盒。**修复**：注入 metrics provider，暴露 `ws_connections{cell}` `ws_messages_total{direction,outcome}` `ws_send_duration_seconds`。**归宿**：029 D7 `PR-V1-WS-OBS-AND-SHUTDOWN-HARDEN`（2026-05-08，原 PR-B2-F1 整体并入 D7）。 | Cx2 |
| ~~**B2-W-04**~~ | ~~WS-MESSAGE-BUFFER-UNBOUNDED~~ | ✅ **closed PR#381（029 A1 PR-V1-SEC-WS-AUTH-ACL）** | `runtime/websocket/hub.go` + `runtime/websocket/conn.go` | per-conn `writeLoop` `select-default-drop`（gorilla chat hub.go 范式）+ slow client（`SendBufferSize`-bounded chan 满时）evict + subjectIdx lockstep 维护 5 个 write point；`Conn.Principal()` interface 暴露；ref: gorilla/websocket examples/chat + centrifugal/centrifuge | — |
| **B2-W-05** | WS-STOP-SYNC-CLOSE-ALL-CONNS | P1 | `runtime/websocket/hub.go:280-293` | Stop 时逐连接同步 Close；千连接场景下 Pod terminationGracePeriod 容易超时。**修复**：广播 close frame 后 fan-out goroutine 池 + 总 deadline；超时强制 conn.Close(）。**归宿**：029 D7 `PR-V1-WS-OBS-AND-SHUTDOWN-HARDEN`（与 D7 WS-OPS-02 同根去重，一次到位）。 | Cx2 |
| ~~**B2-W-06**~~ | ~~WS-DOC-MISSING~~ | ✅ closed PR-V1-SEC-WS-AUTH-ACL | `docs/guides/websocket-integration.md` 新增（9节：架构/Origin配置/认证/心跳/重连/广播/驱逐/故障注入/运维参数表） | — | — |

---

## §7 cmd / 装配 / 启动（8 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-X-01** | OUTBOX-E2E-FIXED-SLEEP | P2 | `cmd/corebundle/outbox_e2e_integration_test.go:169` | 50ms 固定 sleep 等待订阅注册；CI 慢节点 flake；本质等待 ready signal 缺失。**修复**：用 `eventbus.Ready()` 或 `EventRouter.WaitSubscribed(topics)` 同步等待。 | Cx1 |
| **B2-X-02** | SHARED-DEPS-AGGREGATE-TOO-WIDE | P2 | `cmd/corebundle/shared_deps.go:32` | 单结构体含 Topology/JWTDeps/PromStack/EventBus/PGPool/Redis/Claimer/InternalGuard/Addrs 等 ~20 字段；composition root 维护成本高 + 字段间依赖隐式。**修复**：拆 `coreDeps` / `eventDeps` / `httpDeps` / `metricsDeps` 4 组 sub-struct，与 PR-A66 BootstrapDecompose 风格一致。 | Cx3 |
| **B2-X-03** | PG-INVALID-INDEX-WARN-CONTINUE | P2 | `cmd/corebundle/bundle.go:308-313` | invalid index 仅 `slog.Warn` 后继续启动；migration 异常或半完成状态被掩盖；与 readyz 不联动。**修复**：与 B2-A-10 协调，invalid index → readyz 503 → strict mode fail-fast；或加 `WithPGStrictSchema()` 选项。 | Cx2 |
| **B2-X-04** | HEALTH-LISTENER-DEFAULT-LOOPBACK | P1 | `cmd/corebundle/shared_deps.go:461` | 默认 `127.0.0.1:9091`；real-mode 部署若不显式 PodIP / Service 绑定，K8s probe 不可达；现仅注释提醒。**修复**：real mode 缺显式 bind 时 fail-fast；或默认监听 `0.0.0.0:9091` + 治理 archtest 锁定 internal/healthz only via mTLS。 | Cx2 |
| **B2-X-05** | CMD-GENERATE-INDEXES-EXPOSED-NOT-IMPL | P1 | `cmd/gocell/app/generate.go:34` | `generate indexes` 子命令在 help 输出，实现返回 `not implemented`；用户调用得不到能力但又不报"未知命令"。**修复**：从 help 移除（返回 unknown subcommand）；或标注 `[experimental, not yet implemented]` 显示在 help。 | Cx1 |
| **B2-X-06** | GOCELL-VERIFY-CMD-CTX-PROPAGATION | P1 | `cmd/gocell/app/verify.go:101,163,165,241` | `runValidate` 路径已在 PR-V1-030-G03 完成 ctx 透传，但 `gocell verify` 子命令仍硬编码 `context.Background()` 传给 `spec.exec` / `runner.RunActiveJourneys` / `runner.RunJourney` / `generatedverify.Verify`。这些下层调用 `go test` 子进程，NFS/FUSE 慢盘上仍会永久阻塞，与 G-03 修复路径对称缺口。**修复**：(a) 短期 — 与 validate.go boundary 同模式，在 `verifyGenerated` / `verifyJourney` / `runVerifyResult` 入口声明 `ctx := context.Background()` 并向下透传；(b) 彻底 — 改造 `cmd/gocell/app/dispatch.go` 让 `Dispatch` 接 `ctx context.Context`，主入口 `cmd/gocell/main.go` 用 `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` 包装，所有 7 个子命令同步加 ctx 形参。优先做 (b) 一次到位，验证 7 子命令全部可被 Ctrl+C 中断。 | Cx2 |
| **B2-X-07** | GOCELL-DISPATCH-SIGNAL-AWARE-CTX | P1 | `cmd/gocell/app/dispatch.go:20` + `cmd/gocell/main.go:13` | `Dispatch(args []string)` 与 `commands` map 都不接 ctx；`gocell` 进程对 SIGTERM/SIGINT 无优雅关闭路径。CI worker 取消 `gocell validate --strict` 时虽然 G-03 已让内部 ctx 链路完整，但顶层无 signal-bound ctx 注入，end-to-end 取消仍未闭环。**修复**：与 B2-X-06 一并做 — `commands map[string]func(ctx, args) error`；`main.go` 用 `signal.NotifyContext`；测试入口（`mode_test.go` 等）改用 `Dispatch(t.Context(), ...)`。**触发条件**：B2-X-06 启动时合并处理；或 v1.0 GA 前 deployment 路径要求 graceful shutdown 时触发。 | Cx2 |
| **B2-X-08** | CMDRUN-WINDOWS-PROCESS-GROUP-CANCEL | P2 | `pkg/cmdrun/cmdrun_windows.go` (PR-V1-030-G03 Phase E follow-up) | PR-V1-030-G03 Phase E 已在 Unix 上实现进程组取消（`SysProcAttr.Setpgid` + `syscall.Kill(-pid, SIGKILL)`），让 ctx 取消能杀 `go test` 派生的整棵子进程树；Windows 平台退化为 `cmd.Process.Kill()`（与 `exec.CommandContext` 默认行为一致），grandchild 进程仍会泄漏。Linux/macOS CI 路径已闭环；Windows-only 部署或 dev 环境（gocell verify journey 跑 go test）会出现孤儿测试进程。**修复**：用 `golang.org/x/sys/windows.AssignProcessToJobObject` 给子进程分配 Job Object，配 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` flag，ctx 取消时关闭 job handle 杀整组。需要 cmd.SysProcAttr 设 `CREATE_NEW_PROCESS_GROUP` flag。**触发条件**：(a) Windows 成为受支持的 dev/prod 平台（当前 os-smoke matrix 含 windows 但 gocell verify 不在 windows CI 路径）；(b) 用户报告 windows 上 `gocell verify` 取消后 go test 子进程残留。 | Cx2 |
| **B2-X-09** | OUTBOX-FU-COREBUNDLE-NEGATIVE-INTEGRATION | P3 | `cmd/corebundle/bundle.go` + `cmd/corebundle/consumer_base_integration_test.go` (PR-V1-OUTBOX-FU-CLOSURE / N8 won't-do scope-cut) | N8 PR 把 `claiming → lease_id NOT NULL` 升为 DB 级 CHECK 约束并删除 `VerifyOutboxLeaseInvariant` 启动探针后，corebundle 真实 wiring 路径上不再可能产生 NULL lease residue（数据库层先 fail），原计划补的"corebundle 负向集成测试 — NULL lease residue 真实 wiring 阻断启动"沦为不可达分支验证（CHECK 已使该路径不存在）。**修复决策**（按 `feedback_no_backcompat_elegant` + `feedback_radical_self_audit`）：N8 内 won't-do，不补该集成测试。**触发条件**：(a) N3 (`PR-V1-030-G02-ROLLBACK-CTX-DECOUPLE`) 启动时若改造 `cmd/corebundle/bundle.go` startup wiring 顺序（probe / migration / readiness 顺序变化），顺路加一条端到端 startup-failure 集成回归即可；(b) 未来引入 cross-cluster outbox 同步路径（CHECK 不能跨 cluster 守护时）需要回填该测试。**Source**：PR#373/#374 review 二轮闭口讨论（2026-05-05）。 | Cx2 |

---

## §8 contracts 漂移与演进（8 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-T-01** | CONFIG-ROLLBACK-OPTIMISTIC-LOCK-MISSING | P1 | `contracts/http/config/rollback/v1/contract.yaml` + `cells/configcore/internal/ports/config_repo.go:23-25` (TODO 自承) | rollback 无 `expectedCurrentVersion` / If-Match；并发回滚同一 entry 可双写；contract 也无 409 响应声明；代码 `TODO(505-followup)` 已自承。**修复**：(1) request schema 加 `expectedCurrentVersion` required；(2) `UpdateForRollback` SQL 加 `WHERE version=$expected`；(3) contract responses 加 409 `ERR_CONFIG_VERSION_MISMATCH`。 | Cx2 |
| **B2-T-02** | RBACASSIGN-EVENT-CONTRACT-WAIVER-EXPIRY | P1 | `cells/accesscore/slices/rbacassign/contract_test.go:84,93` | `S8-FOLLOWUP (VERIFY-01 waiver expiry 2026-07-01)`；contract test 仅 schema marshal，无真实 outbox publish 路径覆盖；waiver 到期前必须闭合。**修复**：用 `outboxtest.Publisher` mock + assert 真实 emit；移除 waiver 标记。 | Cx2 |
| ~~**B2-T-03**~~ | ~~V1-RESPONSE-SCHEMA-CLOSED-VS-EVOLUTION~~ | ✅ **closed PR#353（029 G5 PR-CI-3-V1-RESPONSE-EVOLVE）** | `contracts/**/v1/response.schema.json` 30 文件 + `kernel/governance/rules_strict.go` FMT-20 | 方向 A 落地：FMT-20 收窄到 request-only（lenient response/event, strict request）；30 response/event schema 顶层 `additionalProperties: true` 放宽；共享 error envelope 例外保持 strict；cell event decoder `DisallowUnknownFields` 同步关闭（B2-C-08）；ADR `docs/architecture/202605031600-adr-v1-schema-evolution.md` + `.claude/rules/gocell/api-versioning.md` 同步 | — |
| **B2-T-04** | CONTRACT-NAMING-USERID-DRIFT | P2 | `contracts/event/user/created/v1/payload.schema.json:6` 等 | path param `{userID}`、payload `userId` 大小写驼峰风格混用；多处不一致。**修复**：统一 camelCase（与 CLAUDE.md JSON/Query/Path 规则一致）；扫全部 contracts 一次性改齐 + archtest 锁定。 | Cx2 |
| **B2-T-05** | INTERNAL-CONTRACT-EXTERNAL-ACTOR-BEARER-DRIFT | P1 | `contracts/http/auth/role/{assign,revoke}/v1/contract.yaml` + boundary.yaml | internal contract 混入 external actor / bearer 语义（与真实 service-token 链路不一致）；与 backlog `T9 HTTP-INTERNAL-CLIENTS-DECLARED-ADV-01` 互补。**修复**：清理 actor / authentication.kind；与 PR-CFG-I X2 boundary 对齐合并处理（PR-CFG-I 未启动 plan）。 | Cx2 |
| ~~**B2-T-06**~~ | ~~CONTRACT-CLIENTS-NOT-COMPILED-INTO-RUNTIME~~ | ✅ **closed PR#362（029 A5 PR-V1-SVCTOKEN-CALLER-IDENTITY）** | `runtime/auth/principal.go` + `runtime/auth/authenticator.go` + contract.yaml | service token claims 加 `caller_cell` + AuthMiddleware 强制 `caller_cell ∈ contract.clients`；同步关闭 PR266-CONTRACT-CLIENTS-RUNTIME-ENFORCE-01。FU-1（accesscore production wiring）/ FU-2（BuiltinServiceRoles 恢复触发条件）已登记 backlog2 行 197-198 | — |
| ~~**B2-T-07**~~ | ~~INTERNAL-SERVICE-TOKEN-CALLER-IDENTITY-COLLAPSE~~ | ✅ **closed PR#362（029 A5 PR-V1-SVCTOKEN-CALLER-IDENTITY）** | `runtime/auth/principal.go` + `runtime/auth/authenticator.go` + claims | claims 加 `caller_cell` 字段；AuthMiddleware 强制 `caller_cell ∈ contract.clients`；`BuiltinServiceRoles` 派生 scope 角色（取代固定 `RoleInternalAdmin`）；同时关闭 B2-T-06；FU-1/FU-2 已登记 backlog2 行 197-198 | — |
| **B2-T-08** | CONFIG-PUBLISH-CONTRACT-FAILURE-CODES-INCOMPLETE | P2 | `contracts/http/config/publish/v1/contract.yaml` | publish 失败语义（404 / 409）未完整声明在 contract.responses；client SDK 不知道处理这些路径。**修复**：补 404 ERR_CONFIG_NOT_FOUND / 409 ERR_CONFIG_VERSION_MISMATCH 到 contract.yaml。 | Cx1 |

### A5 Follow-ups (PR-V1-SVCTOKEN-CALLER-IDENTITY 衍生)

| # | Item | 复杂度 | 触发条件 / 修复方案 |
|---|------|--------|---------------------|
| **B2-T-07-FU-1** | RBACASSIGN-ACCESSCORE-CALLER-PRODUCTION-WIRING | Cx2 | A5 PR 把 rbacassign internal route 挂载并声明 `Clients: [accesscore]`（contract.yaml + spec literal 一致，FMT-18 PASS）。但生产代码中 accesscore 没有 caller-side configclient 风格的 RPC 路径调用 rbacassign — accesscore 在 spec/yaml 是 declared caller，运行时是 unwired caller。这非冲突（不挂没人调用 = 等价于 default-deny），但发布前需要核实预期生产调用是否真为 accesscore。**触发条件**：v1.0 GA 准备阶段。**修复方案**：(a) 若 accesscore setup 流程确实需要调用 rbacassign 完成初始 admin role 分配，按 configclient 模式加 caller-side RPC + 集成测试覆盖；(b) 若 accesscore 不是真实 caller，改 clients 为真实 caller（如 actors.yaml 注册 `external-admin-tool`）并迁移 token 颁发到 issuer-side；(c) 若 rbacassign 无 v1.0 生产用例，删 RouteGroup + clients=[] + 加回 archtest awaitingRealCallerAllowlist 入口 |
| **B2-T-07-FU-2** | BUILTIN-SERVICE-ROLES-REMOVED-FOLLOWUP | Cx3 | A5 PR 删除 `BuiltinServiceRoles`/`RoleInternalAdmin`/`ServiceNameInternal` 三常量，service principal `Roles=nil`、`Subject=""`。**触发条件**：未来若需要 service principal 基于 caller 派生不同 scope（如同一 caller 在不同 endpoint 走不同权限），方案候选：(a) 恢复 `BuiltinServiceRoles(callerCell)` 按 cell 派生 `role:internal:<callerCell>`；(b) 走 `auth.Route.Policy` 路径写自定义 Policy。当前 caller_cell + contract.clients 模型已足；不预先实现 |
| **B2-T-07-FU-3** | K04-CELLGEN-CONTRACTSPEC-CLIENTS-FIELD | Cx2 | A5 PR 给 `wrapper.ContractSpec` 加 `Clients []string` 字段。K#04 cellgen framework (PR#360) 当前模板不生成此字段，未来用 `gocell generate cell` 创建的 internal endpoint cell 可能漏写 Clients 导致 `ContractSpec.Validate()` fail。**触发条件**：K#06 contractgen PR 启动时一并处理；或 K#04 模板下次更新时同步。**修复方案**：cellgen `tools/codegen/cellgen` 模板对 `routeMounts` 中 path 以 `/internal/v1/` 开头的 entry 自动生成 `Clients: []string{...}`（来源 cell.yaml 元数据扩展或注释 marker） |
| **B2-T-07-FU-4** | SVCTOKEN-CALLER-CELL-SHARED-SECRET-LIMITATION | Cx4 | **设计限制**：A5 token 4-part 格式中 callerCell 由 caller 自行声明并用共享 `GOCELL_SERVICE_SECRET` HMAC 签名。HMAC 防止 token 在 cell 边界外被伪造或篡改，**但不防同一 ring 持有者之间的相互冒充**：accesscore 进程持有 ring → 可 mint `callerCell="auditcore"` 的 token 调用 auditcore 的 internal endpoint。`contract.clients` allowlist 因此是 governance-only labeling，不是 anti-spoofing identity boundary（同一 trust domain / assembly 内 cell 间彼此互信前提下可接受）。**触发条件**：(a) 多租户 / 跨 trust-domain 部署场景出现；(b) 安全审计要求 caller identity 必须 credential-derived；(c) 出现内部恶意 cell 威胁模型。**修复方案**：(a) **per-caller HMAC ring** — 每 cell 独立 secret，issuer 仅签发自己的 token（最小改动）；(b) **issuer-issued JWT-SVID** — 中心化 issuer 签发 caller_cell claim 的 short-lived JWT，对标 SPIFFE JWT-SVID（中等改动）；(c) **mTLS** — 每 cell 独立 X.509 证书，AuthorizationPolicy 取 source.principals from peer cert（大改动，对标 Istio）。建议路径：(a) → (b) → (c) 渐进式落地；本 PR 不做。**ADR 参考**：SPIFFE JWT-SVID + Istio AuthorizationPolicy + Consul Service Intentions。|

---

## §9 工时与 PR 拆分建议

### 工时汇总（仅 backlog2 新增项）

| 严重度 | 数量 | 估算工时 | 工作日 |
|---|---:|---:|---:|
| P0 | 6 | ~5d (Cx3-Cx4 多个) | 5 |
| P1 | 40 | ~80h | 10 |
| P2 | 24 | ~30h | 4 |
| **合计** | **70** | **~150h** | **~19 工作日** |

### 推荐 PR 打包（与现有 plan 不冲突，新批次）

```
Wave A — P0 评审先行（先做 ADR / 决策，不直接改）
  PR-B2-A1  WEBSOCKET-AUTH-ARCH-ADR (B2-W-01 + B2-W-02 设计)
  PR-B2-A2  RMQ-RECONNECT-TERMINAL-DESIGN (B2-A-02)
  PR-B2-A3  PG-OUTBOX-CLAIM-FENCING-DESIGN (B2-A-01)
  PR-B2-A4  AUDIT-HASHCHAIN-RECOVERY-DESIGN (B2-C-01)
  PR-B2-A5  REDIS-CLUSTER-SUPPORT-ADR (B2-A-03)
  PR-B2-A6  SETUP-PUBLIC-CLOSURE-ADR (B2-C-02)

Wave B — kernel/runtime 错误语义 + observability 收口（~30h，三路并行）
  PR-B2-B1  KERNEL-WRAPPER-ERRSEMANTIC-AND-CONTRACTTEST (B2-K-01/02/07, ~6h)
  PR-B2-B2  KERNEL-INTERFACE-LIFT-AND-GOVERNANCE (B2-K-03/04/05/06, ~10h)
  PR-B2-B3  OTEL-PROVIDER-LIFECYCLE-AND-GLOBAL (B2-R-05/06/07/08/09 + B2-A-19/20/21, ~14h)

Wave C — adapters 加固（~40h，按 adapter 串行）
  PR-B2-C1  PG-OUTBOX-AND-RELAY-HARDEN (B2-A-04/05/06/07/12, ~8h)
  PR-B2-C2  PG-REFRESH-AND-READYZ (B2-A-08/09/10/11/13, ~12h)
  ~~PR-B2-C3  RMQ-LIFECYCLE-AND-CONFORMANCE~~ → **拆为 029 roadmap B12/B13/B14（2026-05-07）**：B12 LIFECYCLE-HARDEN (B2-A-14/15/16, 10h+5h) / B13 CONFORMANCE-AND-CLOSURE (B2-A-17 + B2-A-18 RMQ 半 + B2-A-29/30 PR#379 FU, 7h+3.5h) / B14 TEST-CLEANUP (PR225-N1 + PR333-RMQ-CLOSE-FLAKE, 2.5h+1.5h)；B2-A-18 PG 半 → B8
  PR-B2-C4  PROMETHEUS-AND-S3-TEST-COVER (B2-A-22/23/24/25 + B2-A-32, ~6h)
  PR-B2-C5  REDIS-MULTITENANT-AND-AUTH (B2-A-26/27/28/29/30/31, ~16h)

Wave D — cells 业务收尾（~20h）
  PR-B2-D1  AUDIT-HARDEN (B2-C-04/05/09/10/12/14, ~10h)
  PR-B2-D2  CONFIG-CONSUMER-AND-EVENT-SCHEMA (B2-C-06/07/08/11, ~6h)
  PR-B2-D3  CELL-INIT-INFRA-LEAK-AND-L2-TEST (B2-C-03/13, ~14h, 含 ADR)
  PR-B2-D4  CONFIGCORE-READYZ-REPO-PROBE (B2-R-02, ~3h, 与 D1 并行)

Wave E — bootstrap/cmd/contracts 收口（~14h）
  PR-B2-E1  BOOTSTRAP-ROLLBACK-AND-HEALTH (B2-R-01/03 + B2-X-04 + PR-CI-5-FU-HEALTH-LATE-WATCHER, ~9h)  # 吸收 backlog HEALTH-LATE-WATCHER-LIFECYCLE-LEAK-01（2026-05-08，同改 kernel/health/*）
  PR-B2-E2  ERRCODE-CLASSIFY-SINGLE-SOURCE (B2-R-04, ~3h)  ✅ closed PR#368
  PR-B2-E3  CMD-COMPOSITION-CLEANUP (B2-X-01/02/03/05, ~6h)
  PR-B2-E4  CONTRACT-DRIFT-FIX (B2-T-01/02/04/05/08, ~10h，与 PR-CI-3 协调)

Wave F — WebSocket 模块加固（**2026-05-08 整体合并到 029 D7 PR-V1-WS-OBS-AND-SHUTDOWN-HARDEN**，本 wave 不再独立）
  ~~PR-B2-F1  WEBSOCKET-OBSERVABILITY-AND-LIMITS~~ → 整体并入 029 D7（B2-W-03 metrics + B2-W-05 stop sync close + WS-HUB-READYZ-PROBE-01 全归 D7；B2-W-05 与 D7 WS-OPS-02 同根去重；详见 029 roadmap Track D D7 行）

Wave G — 运行时授权深度（依赖 Wave A 决策结果，~16h）
  PR-B2-G1  CONTRACT-CLIENTS-RUNTIME-ENFORCE (B2-T-06)
  PR-B2-G2  SERVICE-TOKEN-CALLER-IDENTITY (B2-T-07)
```

### 与现有 plan 的协调

- **PR-CI-3 V1-RESPONSE-EVOLVE**（batch2-k8s-verify）：吸收 B2-T-03，本文件不重复
- **PR-CFG-I AUTH-FAIL-CLOSED-AND-OPS-RESIDUE**（l4 batch2.4）：B2-T-05 / B2-T-07 与其 X1 / X2 主题相关，建议合并到 PR-CFG-I 后续轮次
- **TEST-JOURNEY-ROOT-HARNESS-01**（backlog 主线）：吸收 B2-C-13 / B2-C-14
- **PR266-CONTRACT-CLIENTS-ENFORCE**（backlog P2）：升级为 B2-T-06 + B2-T-07 联合 PR

---

## §10 已修复 / 已在 backlog 的项（参考索引）

### 已修复（最近 PR 关闭，无需登记）

| 编号 | 关闭原因 |
|---|---|
| LY-02 | runner.go:215-222 已 fail-closed |
| LY-07 | auditcore slice.yaml 13 topic 已对齐 |
| LY-09 | audit query queryParams 已声明全字段 |
| LY-12 | listener.go authChain 必填 fail-fast |
| LY-13 | health.go verbose fail-closed |
| LY-15 | EventRouter AddContractHandler error-first |
| LY-16 | worker.go ErrWorkerExitedEarly |
| LY-18 | redis/vault/s3 ValidateTLSEndpoint |
| LY-20 | websocket AllowedOrigins 空 panic |
| LY-21 | postgres refresh_store error-first |
| LY-26 | securecookie 负向测试已覆盖 |
| LY-28 | dispatch.go 改 ContinueOnError + Usage 提示 |
| LY-30 | main_test.go 限定可接受错误 |
| LY-31 | controlplane.go SERVICE_SECRET 全模式必填 |
| LY-33 | actors.yaml + REF-17 已对齐 |
| LY-35 | login/config request 已加 minLength/maxLength/maximum |
| MD-09 | relay.go nil Metrics NoopRelayCollector |
| PR-03 | bootstrap_phases.go validateInternalGuardForDeclaredRoutes |
| PR-12 | health.go verboseDecision fail-closed |
| PR-20 | audit list contract from/to 已声明 |
| PR-24 | controlplane.go SERVICE_SECRET fail-fast |
| CL-11 | access_module.go ForceBootstrap |
| CL-14 | controlplane.go fail-fast |

### 已在 docs/backlog.md（不重复登记）

| 编号 | backlog 条目 |
|---|---|
| LY-04 | `PR252-F2 COMMAND-SWEEPER-PRODUCTION-GOVERNANCE-01` |
| LY-05 | `PR-CFG-G1-FU6` + `V-A11` |
| LY-17 | `PR237-T1 DUAL-LISTENER-TEST-GAPS-01` |
| LY-32 | `T6 GOCELL-PER-CELL-ADAPTER-01` |
| LY-36 | `TEST-JOURNEY-ROOT-HARNESS-01` + `PR-CFG-D-FU` |
| MD-06 | `PR-CFG-A-DEFER-2 CONFIGCORE-L2-MEMORY-MODE-DIVERGENCE-01` |
| MD-10 | `PR266-METADATA-ONLY-CONSUMER-BUSINESS` |
| MD-15 | `PR-CFG-I-DRAIN X1`（plan 未启动）|
| CL-06 / LY-06 | `A26-R2 SETUP-ADMIN-RATE-LIMIT-01`（仅缓解，不根治；本文升级为 B2-C-02）|
| CL-07 / CL-13 | `PR-CFG-I-DRAIN X1`（不含 caller identity；本文升级为 B2-T-07）|
| CL-12 | `PR245-F6/F10` 部分覆盖 |
| PR-10 | `INTERNAL-LISTENER-SERVICE-TOKEN-E2E-HARNESS-01`（部分覆盖）|

---

## §12 PR #376 follow-up 登记

### ARCHTEST-PROJECTMETA-HANDLERS-INDEX-01

**触发条件**：`queryparam_drift` / `auth_plan` / `svctoken_caller_cell` 之外，出现第 4 个 archtest 需要从 `ContractMeta.Endpoints` + 路径模式以外的方式枚举 handler 文件时启动。

**目标**：扩展 `kernel/metadata` 加 `ProjectMeta.Handlers` 索引（key=contractID → list of generated handler paths），让 archtest 不再各自从 `contractIDToExpectedPkgPath` + `os.Stat` 派生路径。

**估时**：6h dev + 3h review

**为什么不在 06.FU3**：当前 3 个 caller（queryparam_drift / auth_plan / svctoken_caller_cell）都已通过 `findCellProductionGoFiles` + `ContractMeta` 间接覆盖；扩 ProjectMeta API 触发面太小，过早抽象。

**Cx**：Cx2

---

### PR-V1-EVENT-TYPED-PAYLOAD-CODEGEN

**触发条件**：当前 17 个 event consumer 各自手写 payload unmarshal + JSON Schema validate（每处 ~30 行重复），扩散到 ≥5 cell consumer 已达升级阈值。

**目标**：contractgen 给 kind=event contract 多产 `payload_gen.go`（typed `Payload struct` + JSON Schema validator）+ subscription.tmpl 升级为 typed handler 入口：

```go
type EventHandler func(ctx context.Context, payload *Payload, entry outbox.Entry) outbox.HandleResult
func NewSubscription(typedH EventHandler, group, slice string) *Subscription
```

内部把 typed handler 封装成 raw EntryHandler（含 unmarshal + payload schema validate）。consumer 删本地 unmarshal/validate 重复。

**估时**：16h dev + 8h review

**依赖**：PR #376（schemavalidate runtime ready）

**对标**：cloudevents/sdk-go event.DataAs / ThreeDotsLabs/watermill components/cqrs typed handler

**Cx**：Cx3（subscription.tmpl + 17 event payload codegen + 17 consumer 调用点）

**为什么不在本 PR**：(a) 工时大不属"范围内紧密相关小工作"；(b) typed payload codegen 是新模块（payload.tmpl + decoder helpers）与本 PR 当前修复无文件交叉；(c) feedback rule "PR 范围切割必须显式 backlog" 适用。

---

## §11 后续动作

1. **本文件作为 backlog2 索引**，不再扩展；条目修复时直接在表格行追加 `✅ #PR` 标记
2. **Wave A 6 个 ADR**优先派 architect agent 评审，确定方向后再派 developer 实施
3. **Wave B-G** 在 ADR 决策完成 + Wave A 主路径修复后启动；可与 `docs/plans/202604290500-backlog-residual-and-merge-roadmap.md` 的 Wave 并行（文件域基本不重叠）
4. backlog.md 中已被 backlog2 升级覆盖的条目（A26-R2 / PR-CFG-I X1 等），合并 backlog2 对应 PR 时同步关闭
