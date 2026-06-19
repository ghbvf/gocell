# Tasks: 运行时契约注册中心（Epic #303 分解）

**Input**: `specs/070-runtime-contract-registry/spec.md` + `research.md`
**Prerequisites**: 探索结论（research.md）已确认 GoCell 现状（ContractRegistry 只读 / eventrouter Run-once / governance.Validator 纯 Go）。
**Organization**: 按 user story 分组；每个 story = 一个 GitHub backlog issue = 一个（或一簇）PR，净增删 ≤2000 行（US14/US18 特殊可超）。issue 号在登记后回填。
**Tests**: 本仓约束（TDD、kernel ≥90% / 其余 ≥80%、archtest synthetic red case、contract-level 测试）对每个 story 默认生效。

## Format: `[ID] [P?] [Story] Description`

- **[P]**: 与同 phase 其它任务无文件交叉、可并行
- 路径为当前锚点，实施期以 US1 ADR 裁决为准

## Phase 1: 方向裁决 + 数据面地基（Wave 1，无 blocker，立即可做）

### US1 — 方向 ADR：dual-track + 进程外控制面/数据面转发（P1）→ #2230

- [ ] T001 [US1] 写 ADR `docs/architecture/{ts}-303-adr-runtime-contract-registry.md`：dual-track reconciliation（in-tree Hard trust root 不动 / out-of-tree runtime governance gate = Medium，**不伪装 Hard**）+ 进程外控制面/数据面转发设计 + 完整威胁矩阵（每行配补偿 story 指针）
- [ ] T002 [P] [US1] ADR 显式声明扇出 archtest（DEAD-CONTRACT-01 / IMPL-DECL-COVER-01 / EMIT-DECL-COVER-01 / DEAD-CODE-01）对 runtime 注册契约 vacuous = 设计预期 + out-of-tree 等价 runtime 扇出校验方向
- [ ] T003 [P] [US1] 与 #1081/#1090（Tier-1）/#1046（codegen schema）/#1423（remote transport）边界划清 + 与 #1081 方向 ADR 对齐；AI-robust 章程：冲突段落同改

### US9 — EventRouter handlers 容器 slice→map（地基重构，P1）→ #2231

- [ ] T010 [US9] `framework/runtime/eventrouter/router.go`：`handlers []handlerConfig` → `map[handlerKey]*runningHandler`（key=topic+consumerGroup）+ `runningHandler` 结构（cfg/cancel/started/done）；`Run`/`HandlerCount`/`AddContractHandler` 改 map；重名返 error（不 panic）
- [ ] T011 [P] [US9] 现有 eventrouter 测试零回归（行为等价）+ 重名 key error 用例

**Checkpoint**: ADR 合入后下游形态锁定；US9 为 US10 的数据结构前提

## Phase 2: 内核地基（Wave 2，blocked-by US1）

### US2 — Runtime ContractRegistry 只读→状态机（P1）→ #2233

- [ ] T020 [US2] `framework/kernel/registry/`：sealed `RegistrationState`（submitted…retired）+ 状态迁移 + 不变式（approved 才能 active；reject 不可激活）；包外不可构造状态
- [ ] T021 [US2] append-only 迁移事件底模 + 投影 in-mem 索引；保留现有 deepCopy serving 读路径（Get/ByKind/ByOwner/Provider/Consumers 零回归）
- [ ] T022 [P] [US2] kernel ≥90% table-driven：每条合法/非法迁移 + sealed 构造拒绝 + 投影一致性

## Phase 3: 注册门 + cell 骨架（Wave 3，blocked-by US2）

### US3 — 注册时 governance gate（复用 Validator + dry-run/submit 双入口，P1）→ #2234

- [ ] T030 [US3] 包 `governance.Validator` 为 `{allowed,result,warnings}`（AdmissionResponse 式）；`:check`(dry-run 不落库) + `submit`(落库前同步校验) 双入口共用校验逻辑
- [ ] T031 [P] [US3] FailurePolicy=Fail fail-closed（校验器/租户/store 不可用 → deny）+ 测试
- [ ] T032 [US3] **runtime 扇出完整性校验**（ADR 承诺的等价 runtime 补偿）：注册时验 publisher/subscriber/owner 齐全（扇出 archtest 对 runtime 契约 vacuous 的 register-time 补偿）+ 缺 publisher/subscriber/owner 的 fail-closed 测试（与 US15 命名空间/ceiling 校验同走 gate）

### US4 — registrycore cell 骨架 + submit/list 契约声明（P1）→ #2235

- [ ] T040 [US4] `corecells/registrycore/` cell.yaml + slices + `http.registry.contract.submit.v1`/`list.v1` 契约声明（contracts/）+ contractUsages
- [ ] T041 [US4] cellgen 派生 `cell_gen.go` + golden；`gocell validate` 通过
- [ ] T042 [P] [US4] 契约级测试：正常响应 schema / 参数错误码 / 鉴权边界 / path 参数校验

## Phase 4: 持久化 + 提交闭环（Wave 4，blocked-by US3+US4）

### US5 — contract_registrations 表 + PG store（P1）→ #2236

- [ ] T050 [US5] migration `adapters/postgres/migrations/{序号}_create_contract_registrations.sql`（id/submitter/kind/payload-schema/state/approver/timestamps；RLS/owner 形态）
- [ ] T051 [US5] repository：`internal/ports` 接口 + `internal/mem` + PG 实现；状态迁移 L1 事务 + append-only 历史
- [ ] T052 [P] [US5] 事务完整性测试（L1 原子性 / 回滚不留半态）

### US6 — submit/list handlers + service 接线 + 契约测试（P1）→ #2237

- [x] T060 [US6] submit/list handler（typed response envelope）+ application service（`gocell:"required"` 依赖）接 US2 状态机 / US3 gate / US5 store；list 强制分页（`limit`≤500 截断 + `data`/`nextCursor`/`hasMore` envelope，per go-standards；复用现有 query pagination helper）
- [x] T061 [P] [US6] httptest：submit（合法→pending / 非法→4xx shared error schema）+ list（按 state 过滤 + cursor 翻页 + limit 上限截断）端到端闭环

## Phase 5: 审批 + 审计/事件（Wave 5，blocked-by US6）

### US7 — Admin 审批 approve/reject/retire + RBAC（P2）→ #2238

- [x] T070 [US7] `approve.v1`/`reject.v1`/`retire.v1` 契约 + handler；路由门禁经 `endpoints.http.permission` overlay → `auth.RequirePermissionForContract` 接 accesscore admin（permission-based PDP，不硬编 role）。附带：submit/list 一并迁出冻结 modeless ledger 到同一 resolver 范式（#2238）
- [x] T071 [P] [US7] 状态机不变式（conformant→pending-approval→approved；reject 不可激活；active→retired 终态）+ 非 admin fail-closed 测试（approve/reject/retire 三端点对称）
- [x] T072 [US7] retire：`active→retired` 迁移（本 US 交付）；contract test 覆盖 retire handler + 非 admin deny + 非法迁移（400）。审计 + `contract-retired` 事件归 US8（T080/T081），数据面 remove 归 US11/US12——非本 US 范围（FR-001 五端点闭合）

### US8 — 审批审计落账 + 激活/退役事件（P2）→ #2239

- [ ] T080 [US8] submit/approve/reject/retire → auditcore hash chain（replayable PII hash/redaction）
- [ ] T081 [US8] `event.registry.contract-activated.v1`/`contract-retired.v1`（L2 OutboxFact，envelope 经 outbox.NewEntry）
- [ ] T082 [P] [US8] L2 outbox 原子性 + consumer 幂等测试

## Phase 6: 数据面动态加载（Wave 5-6，event 维度 blocked-by US9，HTTP 独立）

### US10 — 运行时增删订阅：per-handler ctx + Add/Remove（P1）→ #2240

- [ ] T100 [US10] `runRootCtx` 字段提升 + `AddRunningHandler`/`RemoveHandler`：per-handler 子 ctx（挂 runRootCtx）、复刻 Setup→Subscribe→await Ready、fail-closed 半启动清理（hcancel+不入map+不Inc）
- [ ] T101 [US10] 与 `runGuard sync.Once` 兼容（Run 仍一次；增删仅 started 后合法，否则 ErrNotRunning）；`Close` 三阶段 drain 复用
- [ ] T102 [P] [US10] 测试：Run 后 add 新 topic 消费 / remove 单 handler 退出不影响他者 / Add 失败无半启动 / Close 停动态 handler

### US11 — Snapshot 版本化 diff Reconcile + 激活事件 debounce + archtest（P2）→ #2241

- [ ] T110 [US11] `RegistrySnapshot` 单调 Version + `Reconcile(snapshot)` diff（toAdd/toRemove）；版本回退 fail-closed 保 last-good
- [ ] T111 [US11] `contract-activated` 消费侧 debounce（DebounceAfter+debounceMax，merge 窗口内多激活为一次 reconcile）
- [ ] T112 [US11] archtest 守卫（动态 handler 子 ctx 挂 runRootCtx / Add fail 必 hcancel）+ synthetic red/green（同 PR 自证，constraint-self-close）

### US12 — HTTP Router 运行时动态 route（P2）→ #2242

- [ ] T120 [US12] HTTP 数据面：一次性 drain → 运行时 add/remove route（net/http ServeMux 包装/可替换 mux）；消费 contract-activated 登记外部 cell route
- [ ] T121 [P] [US12] 与 FinalizeAuth/listener auth plan 兼容（动态 route 仍过最终 matcher，不绕 auth）+ 并发 add/remove 测试

## Phase 7: 外部接入 + 转发（Wave 6+，blocked-by US6/#1423）

### US13 — 外部 cell 注册客户端 / SDK（P2）→ #2243

- [ ] T130 [US13] 契约声明 helper + payload schema 上传 + submit/poll-status client（对标 SR RegisterSchemaRequest）
- [ ] T131 [P] [US13] 错误机读可区分（typed reason，非英文文本）+ 测试

### US14 — 端点注册 + 数据面转发（P2，**依赖 #1423/#1966**，特殊可超 2000 行）→ #2244

- [ ] T140 [US14] cellID→endpoint 地址登记 + 复用 #1423/#1966 `CellTransport` remote + Resolver 转发 topic 事件/HTTP 请求（service token 出站签名 + principal/tenant 传播）
- [ ] T141 [P] [US14] 双进程 fixture：approve 后转发到外部端点 + principal/tenant 重建 + 端点不可达映射专属 errcode（复用 #1423 FR-007 投影约束）

## Phase 8: 安全与隔离（Wave 6+，blocked-by US6）

### US15 — 命名空间防冲突 + consistency ceiling（P2）→ #2245

- [ ] T150 [US15] 注册唯一性 `(kind,domain-path,version,owner)` 四元组（对标 CRD NamesAccepted）+ 外部契约命名空间前缀；与 in-tree 契约撞车拒绝
- [ ] T151 [P] [US15] consistency 越权：runtime cell 声明级别经 actors.yaml `maxConsistencyLevel` runtime 形态裁决，超限 fail-closed

### US16 — 恶意注册防护 + 限流 + endpoint egress allowlist（P2）→ #2246

- [ ] T160 [US16] 注册端点鉴权 + 限流（防重复/恶意注册，429/稳定 errcode）
- [ ] T161 [US16] **统一 endpoint egress allowlist admission**：一切向提交方端点的出站——conformance 探测（US17）+ US14 数据面转发 + ExternalEndpoint 注册——同走一条 admission（egress allowlist + 鉴权 + 限流，禁裸打/内网 metadata 地址）；数据面只消费 admitted endpoint
- [ ] T162 [P] [US16] 测试：超频限流 / conformance 或 US14 转发非 allowlist 目标拒绝 / 内网 metadata 地址拒绝 / 未授权 submit 拒绝

## Phase 9: 注册 conformance 自动测试（flag-cond，分档）

### US17 — conformance 首档：读/幂等面 + setup/teardown（P2，flag-cond，blocked-by US7 + US16）→ #2247

- [ ] T170 [US17] `probing` 态机器门：合成 consumer 按契约 request schema 打活体端点（读/幂等面），断言 successStatus+响应 schema+声明 4xx/5xx 可达+error envelope 合规
- [ ] T171 [US17] setup/teardown sealed 配对（缺 teardown 校验/编译错，AI-robust Hard）+ 凭据注入不入契约
- [ ] T172 [P] [US17] conformance result append-only record（admin 据此审批）+ 测试

### US18 — conformance 彻底档：合成租户写面 + egress-suppression（P3，flag-cond，**hard-depend #1337**）→ #2248

- [ ] T180 [US18] 合成租户跑真实写路径 conformance（复用 tenant 隔离边界）+ 测完整租户清除 + event-consumer 写面深测
- [ ] T181 [US18] 合成租户 egress-suppression 标记（禁真实外发）；非多租户 cell 退回首档 (c)+admin 签字（混合形态）

## Dependencies & Execution Order

```
US1(ADR) ─┬─ US2(SM) ─┬─ US3(gate) ─┐
          │           └─ US4(cell) ─┴─ US5(store) ─ US6(submit闭环) ─┬─ US7(approve) ─ US8(audit/event) ─┐
US9(map) ──── US10(add/remove) ───────────────────────────────────── US11(reconcile) ←──────────────────┘
          └─ US12(http route)                                         │
                                              US6 ─┬─ US13(SDK) ─ US14(转发) ←── #1423/#1966 + US16(endpoint egress admission)
                                                   ├─ US15(namespace/ceiling) ─ US16(防护/endpoint egress)
                                                   └─ US7 ─┬─ US17(conformance 首档, flag-cond) ←── US16
                                              #1337 + US14 + US17 ─ US18(conformance 彻底档, flag-cond)
```

| 实施波次 | Story | 性质 |
|------|-------|------|
| W1 | US1（ADR）、US9（map 重构） | 裁决 + no-regret 地基，立即并行 |
| W2 | US2（registry 状态机） | 内核地基，blocked-by US1 |
| W3 | US3（gate）、US4（cell 骨架） | blocked-by US2，可并行 |
| W4 | US5（store）、US10（add/remove）、US12（http route） | 持久化 + 数据面增删（US10/US12 blocked-by US9，可与 US5 并行） |
| >W4（Project 超窗，暂不入 Wave 字段） | US6（提交闭环）、US7（approve）、US8（audit/event）、US11（reconcile）、US13（SDK）、US15（namespace）、US16（防护）、US17（conformance 首档 flag-cond） | 依赖链 >4，滚动前移；前置完成后自动入 Wave 字段 |
| gated | US14（转发，gated #1423）、US18（conformance 彻底档，gated #1337） | 外部 epic 依赖 |

> Project Wave 字段固定 Wave 1-4 四档（issues 技能 Part A 滚动写入；超窗 OPEN 不入字段、前置完成后自动前移）。本表是派生视图。

## Notes

- 每 story 一个 issue、一个（或一簇）PR，各自走 `/ship`（worktree + TDD + 内置 review）。
- US14 数据面转发不重复造跨进程同步栈：复用 #1423/#1966 remote transport（blocked-by 其 remote 就位）。
- US18 conformance 彻底档 hard-depend 多租户/ABAC epic #1337 GA；首档 US17 不依赖多租户。
- runtime governance gate AI-robust = Medium（runtime guard + 人审），P0 ADR 威胁矩阵显式记录，不伪装 Hard。
- 原始诉求 #659（WithExtraTopics）被本 epic 取代，分解落地后 close as superseded by #303。
