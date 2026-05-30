# Epic: 运行时契约注册中心（Runtime Contract Registry + Admin 审批）

> 来源：issue #303 升级。状态：设计草案（方向已对齐 → ADR → 分阶段 PR）。
> **架构形态已定：进程外控制面**（§4）。

## 1. #303 原始诉求与升级动机

#303（pri-p2）原始抱怨：平台 cell 把订阅 topic「写死」，业务方发新 topic 不会被自动消费，提出加 `WithExtraTopics` option。

**该诉求的前提已过时**：探查代码发现 auditcore 早已没有 `Topics` 数组 / `auditAppendSpecs` map；订阅现在由 cellgen 从 `slice.yaml` 的 `contractUsages[role=subscribe]` **单源派生**进 `cell_gen.go`，`cellID` 是 `Registrar.Subscribe` 第 4 个位置必填参数（编译期 HARD 契约，ADR 202605111000）。所以 `WithExtraTopics` 方向上与当下框架收口哲学相反——它把已收口成 codegen-funnel 的静态订阅重新打开成 runtime 可变 option。

**升级动机（产品决策）**：把"扩展订阅"从一次性编译期 option，提升为一个**持续运行的平台能力**——框架上线后保持运行，外部 cell 通过 API 随时提交契约注册，framework admin 审批通过后契约即上线生效，无需重新编译/部署框架二进制。

## 2. 愿景与语义澄清

本 epic 实现的是 **运行时动态注册中心**（区别于"构建期外部模块 onboarding"）：

```
外部 cell ──submit契约──▶ registrycore ──governance校验──▶ pending
                                                            │
                              admin ──approve──▶ active ────┤
                                                            ▼
                              EventRouter / HTTP Router 动态加载该契约的订阅/路由
```

状态机：`submitted → probing → (conformant → pending-approval → approved | rejected) → active → retired`。`probing` 是注册 conformance 自动测试态（机器门，独立 Phase P7，见 §6.1）——在 admin 人审之前自动验证活体 cell 是否兑现其声明的契约；首档（L1 静态 +(c) 读面）不依赖多租户，可早上，写面深测的彻底形态 hard-depend 多租户 GA。

## 3. 核心张力与 reconciliation（dual-track，必须诚实对齐宪法）

GoCell 第一性原理是 *AI-robust*：约束做到「违反不可表达」，靠 codegen funnel + 编译期 type system + 静态 archtest。整个契约扇出闭环（`DEAD-CONTRACT-01` / `IMPL-DECL-COVER-01` / `EMIT-DECL-COVER-01` / `DEAD-CODE-01`）都依赖**契约在编译期可被静态枚举**。

运行时注册把契约的「权威源」从编译期 YAML 搬到 runtime registry，**对动态注册的那部分契约，编译期静态治理结构上不可达**。不能假装没有这个张力。reconciliation 原则是 **双轨制**：

| 轨道 | 治理方式 | 信任根 |
|------|---------|--------|
| **In-tree cells**（框架自带 + 编译进二进制） | 现有编译期静态治理（cellgen / archtest 扇出闭环 / `gocell validate`）**全部保留不变** | build-time，framework 自身的 trust root |
| **Out-of-tree cells**（运行时注册） | 编译期治理不可达 → 由 **runtime governance gate 补偿**：注册时跑同一套 `kernel/governance` 规则 + admin 审批 state machine + 审计落账 | register-time + admin 人审 |

**关键洞察**：`kernel/governance.Validator` 与 `kernel/registry.ContractRegistry` 本就是纯 Go runtime 代码，今天只是被 `gocell validate` CLI 调用。把它们接到一个注册 API 上即可 runtime 化——**runtime 注册校验 ≈ 把 `gocell validate` 从 CI 搬到注册端点**。admin 审批是替代编译期 CI gate 的人工补偿控制。

> AI-robust 评级影响：本 epic 引入的是**运行时 governance gate（Medium 形态：runtime invariant guard + 人审）**，不是 Hard 编译期收口。这是 out-of-tree 契约在 Go 下能达到的天花板，必须在 ADR 威胁矩阵中显式记录，不得伪装成 Hard。

## 4. 架构形态：进程外控制面（**已定**）

外部 cell 是独立进程/服务；注册中心管理的是它的**契约元数据 + 订阅意图 + 端点地址**。

- 框架运行时退化为「**契约控制面 + 事件/HTTP 数据面转发**」：in-tree 二进制保持完全静态可验证，out-of-tree 只贡献元数据与网络端点。
- 范式对标：K8s CRD + API aggregation layer / Envoy xDS（service mesh 控制面）；与 GoCell「cell 间只经 contract 通信」「examples 是自包含 mini-assembly」一脉相承，进程边界提供天然隔离。

> P0 ADR 只需展开 dual-track reconciliation 论证 + 数据面转发设计 + 威胁矩阵。

## 5. 架构：新增平台 Cell `registrycore`

注册中心本身应实现为一个新平台 cell（与 accesscore/auditcore/configcore 并列），吃自己的狗粮：

- **HTTP 契约**：`http.registry.contract.submit.v1`、`http.registry.contract.approve.v1`、`http.registry.contract.list.v1`、`http.registry.contract.retire.v1`
- **持久化**：`contract_registrations` 表（id / submitter / kind / payload-schema / state / approver / timestamps），状态机迁移走 L1 本地事务
- **事件**：`event.registry.contract-activated.v1` / `event.registry.contract-retired.v1`（L2 OutboxFact）
- **一致性**：注册写入 L1/L2；router 消费 activated 事件做动态加载属 L3（WorkflowEventual）
- **RBAC**：approve/reject 端点接 accesscore，要求 admin 角色
- **审计**：每次 submit/approve/reject/retire 经 auditcore 落 hash chain（审批可追溯是产品硬需求）

EventRouter / HTTP Router 需从「启动期一次性 drain」升级为「**支持运行时 add/remove subscription/route**」——这是数据面最大改造，goroutine 生命周期与并发安全是难点。

## 6. 分阶段 PR 拆解（草案）

| Phase | 标题 | 产出 | 依赖 |
|-------|------|------|------|
| **P0** | 方向 ADR | dual-track reconciliation 论证、进程外控制面数据面转发设计、威胁矩阵、与扇出 archtest 关系 | — |
| **P1** | Runtime ContractRegistry 可变化 | `kernel/registry.ContractRegistry` 从只读 → register/approve/activate/retire 状态机；复用 `kernel/governance.Validator` 做注册时校验 | P0 |
| **P2** | registrycore cell + 持久化 | 新平台 cell、`contract_registrations` 表、submit/list 契约、PG store + conformance | P1 |
| **P3** | Admin 审批工作流 | approve/reject 端点 + RBAC（accesscore admin）+ 审计落账（auditcore）+ 状态机不变式 | P2 |
| **P4** | 数据面动态订阅 | EventRouter 运行时 add/remove handler（消费 contract-activated）；HTTP Router 动态 route；goroutine 生命周期安全 | P2 |
| **P5** | 外部 cell 注册客户端 / SDK | 外部 cell 如何声明契约、上传 schema、调注册 API；端点注册（进程外模型下的地址登记） | P3,P4 |
| **P6** | 安全与隔离 | 运行时注册 cell 的 consistency ceiling、tenant 隔离、topic 命名空间防冲突、恶意/重复注册防护、限流 | P4,P5 |
| **P7** | 注册 conformance 自动测试（独立，见 §6.1） | `probing` 态机器门：活体 cell 契约 conformance 握手。**首档** L1 静态 +(c) 读面，可早上；**彻底档** (b) 合成租户写面深测 + egress-suppression，**hard-depend 多租户 GA（epic #1337）** | 首档 P3；彻底档 多租户 GA + P4 |

## 6.1 注册 conformance 自动测试（独立 Phase P7）

**前提**：外部 cell 是**已部署、正在运行**的活体服务；注册 = 活体请求加入网格。`submitted → active` 之间是**隔离态（quarantine）**——已注册但收不到任何生产流量/事件。P7 在这个隔离窗口对活体实例做 contract conformance 握手，作为 admin 人审之前的机器门。对标 Envoy「加入 LB 池前 active health check」/ Pact provider verification / K8s readiness gate。**这不是测代码，是验活体黑盒兑现契约。**

**两类握手**：

- **HTTP-provider 契约**：框架扮合成 consumer，按契约 request schema 造请求打活体端点，断言 `successStatus` + 响应 schema + 声明 4xx/5xx 可达 + error envelope 合规。
- **Event-consumer 契约**：框架往**隔离测试通道**发合成事件（只该注册 cell 消费），验 disposition 语义（Ack/Requeue/Reject）；测试事件带 sandbox 标记 / 走 quarantine topic，不漏进真实下游。

**核心难点 = 副作用**：活体写端点打下去是真实写库。三方案中 **(b) 合成租户为目标形态**（决议）：

| 方案 | 走真实写路径 | 要 cell 配合 | 彻底度 |
|------|:---:|:---:|------|
| (a) dry-run 模式 | ❌ 旁路分支 | 要 | 中（旁路可能与真路径漂移） |
| **(b) 合成租户**（目标） | ✅ 真 handler/真落库 | 几乎不要 | **最高**（复用 tenant 隔离边界，不发明 conformance 专用旁路） |
| (c) 只测读/幂等面 | 写面不覆盖 | 不要 | 低（首档兜底） |

**(b) 的硬依赖**：其安全保证（合成租户数据隔离 + 整租户可清除）**完全建立在 tenant 隔离边界为真**之上。多租户未 GA 时「合成租户」只是名字 → (b) 结构性 block 在多租户 GA 之后（依赖多租户/ABAC epic **#1337**，工作分支 `docs/800-tenancy-abac-design`；P7 彻底档是 tenancy 隔离原语的消费方）。

**两个 caveat**（P7 设计须解）：

1. **非多租户注册 cell**：(b) 假设 cell tenant-scoped；不分租户的 cell 无处放合成租户 → 退回 (c)+admin 签字。最终形态是**混合**：tenant-aware 走 (b)，其余走 (c)。
2. **tenant 只隔离数据、不隔离 I/O**：合成租户挡得住「写自己的库」，挡不住 handler 对外发事件/调外部系统/发邮件。严格 (b) 需合成租户附 **egress-suppression 标记**（合成租户请求禁真实外发）才算真隔离。

**诚实边界**：握手只验**契约可观测面**（schema/status/disposition），**验不了业务逻辑正确性**，也无法一次握手证明幂等/exactly-once（那需持续对抗性重投）——首档只给 smoke 级。

**分档解耦**（不让自动测试拖累 registry 主体）：首档 L1 静态 governance +(c) 读/幂等面 conformance，今天的 contract schema + `tests/contracttest` 范式即可撑，随 P3 上；彻底档 (b)+egress-suppression 覆盖写面与 event-consumer 深测，gated on 多租户 GA。

## 7. 与现有扇出 archtest 的关系

`DEAD-CONTRACT-01` / `IMPL-DECL-COVER-01` / `EMIT-DECL-COVER-01` / `DEAD-CODE-01` 等扇出闭环 invariant **只覆盖 in-tree 静态契约**，对 runtime 注册契约 vacuous——这是**设计预期**，不是缺陷。必须在 P0 ADR 显式声明这些 archtest 的 scope 边界，并为 out-of-tree 契约提供等价的 **runtime 扇出校验**（注册时验证 publisher/subscriber/owner 完整性，复用 governance 规则的 runtime 形态）。

## 8. 威胁矩阵 / 风险（P0 ADR 需完整展开）

- **恶意注册**：未授权方注册伪造契约 → admin 审批 gate + RBAC + 注册端点鉴权 + 限流。
- **topic / 路由命名空间冲突**：runtime 注册契约与 in-tree 契约 ID/topic/HTTP path 撞车 → 注册时唯一性校验 + 命名空间隔离（如外部契约强制前缀）。
- **consistency 越权**：runtime cell 声明超出其 actor ceiling 的一致性级别 → 复用 actors.yaml `maxConsistencyLevel` 语义的 runtime 形态。
- **数据面动态加载并发**：add/remove subscription 与正在消费的 goroutine 竞争 → Router 生命周期管理（参照 Watermill router 动态 handler）。
- **审批旁路**：active 态契约绕过审批直接生效 → 状态机不变式（只有 approved 才能 active）+ 审计强制落账。
- **conformance 探测 SSRF / 出站攻击面**（P7）：框架主动向"提交方声明的任意端点"发请求 → egress allowlist + 鉴权 + 限流，禁裸打。
- **合成租户副作用泄漏**（P7-(b)）：合成租户请求触发真实外发（事件/外部系统/邮件）→ egress-suppression 标记；合成租户数据漏进共享态 → 依赖多租户 GA 的隔离边界 + 整租户可清除。
- **AI-robust 降级**：runtime gate 是 Medium 不是 Hard → 显式记录，不伪装；in-tree 轨道 Hard 不动摇。

## 9. 验收信号（epic 级）

- 外部 cell（进程外）能 submit 一个 event 契约 → admin approve → 框架**不重启**即开始转发该 topic 事件给该外部端点。
- 未审批契约不生效；reject 后契约不可激活。
- 全程审计可追溯（auditcore hash chain 含 submit/approve/activate）。
- in-tree 静态治理（archtest 扇出闭环 + `gocell validate`）**零回归**。
- P0 ADR 威胁矩阵逐行有补偿措施。

## 10. 参考范式（P0 调研）

| 关注点 | 对标 |
|--------|------|
| 运行时 schema 注册 + 审批 | Confluent Schema Registry（compatibility check + subject 版本） |
| 控制面/数据面分离 | Kubernetes CRD + API aggregation layer / Envoy xDS |
| 动态 handler 路由 | Watermill router 动态 add handler |
| 外部端点注册 + 转发 | service mesh 控制面（Istio Pilot 范式） |

---

> 下一步：P0 方向 ADR，再逐 Phase 立 sub-issue 挂本 epic。
