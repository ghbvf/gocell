# 内核约束报告 — Phase 2: Runtime + Built-in Cells

## 审查人: Kernel Guardian
## 日期: 2026-04-05

---

## (a) 内核集成修改建议

### KG-1: audit-core 缺少事件订阅 contractUsages 声明

- **严重度**: 高
- **类别**: 契约完整性
- **问题**: spec FR-9 明确要求 audit-core 消费 6 种事件（event.session.created.v1 / event.session.revoked.v1 / event.user.created.v1 / event.user.locked.v1 / event.config.changed.v1 / event.config.rollback.v1），但审查 audit-core 全部 4 个 slice.yaml，没有任何一个声明 `role: subscribe` 的 contractUsage。同时 event.config.changed.v1 的 subscribers 列表中仅有 access-core，不包含 audit-core；event.config.rollback.v1 的 subscribers 虽包含 audit-core，但无对应 slice 声明 subscribe。这会导致：
  1. `gocell validate` 的 TOPO-03 规则检测到 consumer-role 不在 contract subscribers 中
  2. audit-core 的事件消费在运行时无法路由
- **建议**: 
  1. 在 audit-core 下新增或复用某个 slice（如 audit-append）声明 contractUsages subscribe 角色，覆盖所有 6 个事件 contract
  2. 同步修改 event.config.changed.v1 / event.user.created.v1 / event.user.locked.v1 / event.session.created.v1 / event.session.revoked.v1 的 subscribers 列表，确保包含 audit-core
  3. 修改 event.config.rollback.v1 contract 中已有 audit-core subscriber，但需对应 slice 声明 subscribe role

### KG-2: config-subscribe slice 缺少 subscribe contractUsage

- **严重度**: 高
- **类别**: 契约完整性
- **问题**: config-core 的 config-subscribe slice.yaml 的 contractUsages 为空数组 `[]`，但 spec FR-10 明确要求该 slice 消费 event.config.changed.v1。同时 event.config.changed.v1 contract 的 subscribers 中是 access-core 而非 config-core，语义不一致——config-core 应当订阅自己发布的配置变更以更新本地缓存吗？需要明确设计意图。
- **建议**: 
  1. 如果 config-subscribe 是给 config-core 自身使用，则在 event.config.changed.v1 的 subscribers 加入 config-core，并在 slice.yaml 声明 `role: subscribe`
  2. 如果 config-subscribe 是通用配置下发机制（由其他 Cell 通过依赖注入使用），需在 spec 中明确其职责边界，避免同一 Cell 既 publish 又 subscribe 同一个 event contract 造成循环

### KG-3: http.auth.me.v1 contract 无 serving slice 声明

- **严重度**: 中
- **类别**: 契约完整性
- **问题**: 存在 contract `http.auth.me.v1`（ownerCell: access-core），但 access-core 的 7 个 slice 中没有任何一个声明 `contractUsages: [{contract: http.auth.me.v1, role: serve}]`。spec FR-8 的 slice 列表中也没有专门的 "me" 或 "profile" slice。这是一个孤立 contract——有声明但无提供者 slice。
- **建议**: 
  1. 在 session-validate 或 identity-manage 的 slice.yaml 中添加 `http.auth.me.v1` 的 serve 声明
  2. 或在 spec 中新增对应的 slice 说明
  3. 如果此 contract 是 Phase 2 范围外，标记 lifecycle 为 draft

### KG-4: Cell 接口缺少 Slice Init 编排与依赖注入通道

- **严重度**: 高
- **类别**: 核心约束
- **问题**: 当前 kernel `Cell.Init(ctx, Dependencies)` 仅传入 `Dependencies{Cells, Contracts, Config}`，但 Phase 2 的 Cell 实现（如 access-core）需要向 Slice 注入 domain service、repository port 接口等。`Slice.Init(ctx)` 签名不接受任何依赖参数。这意味着：
  1. Cell.Init 内部必须手工构造每个 Slice 的依赖并调用 Slice.Init，但 Slice 接口不支持传递依赖
  2. 实际实现中要么绕过 Slice 接口直接操作具体类型（违背接口抽象），要么在 Slice 构造函数中注入依赖（Init 变成空操作）
  3. BaseSlice.Init 是 no-op，嵌入 BaseSlice 的具体 Slice 需要 override Init，但无法接收 service 层依赖
- **建议**: 
  1. 方案 A：扩展 Slice 接口为 `Init(ctx context.Context, deps SliceDependencies) error`，其中 SliceDependencies 携带 cell 级共享资源
  2. 方案 B：保持 Slice.Init 签名不变，但在 NewXxxSlice 构造函数中注入依赖（Init 仅做状态检查），在 spec 中明确此模式
  3. 推荐方案 B，与 Uber fx 的构造时注入风格一致，但需在 spec 和 Cell 开发指南中明确约定

### KG-5: Assembly 缺少 runtime/ 集成点

- **严重度**: 高
- **类别**: 分层隔离
- **问题**: 当前 CoreAssembly 只管理 Cell 生命周期（Register/Start/Stop/Health），但 spec FR-3 的 Bootstrap 启动流程为 `parse config -> init assembly -> register cells -> start HTTP server -> start workers -> block until signal`。Assembly 与 HTTP server、worker、config watcher 之间缺少集成接口：
  1. Assembly.Health() 返回 `map[string]HealthStatus`，FR-1.2 的 `/healthz` 需要调用它，但 Assembly 接口定义在 kernel/，runtime/http/health 需要 import kernel/ 来获取此类型——这是合规的（runtime/ 可依赖 kernel/），但需确认依赖方向
  2. Assembly 不管理 HTTP server 和 worker 的生命周期，Bootstrap 必须在 Assembly 外层自行编排
  3. Assembly.Start 内部调用 Cell.Init 构建的 Dependencies.Config 目前是空 map，与 runtime/config 模块的配置加载无集成
- **建议**: 
  1. 确认 runtime/ 可以 import kernel/cell 和 kernel/assembly 的类型（CLAUDE.md 未禁止 runtime/ 依赖 kernel/，但也未明确允许）
  2. 在 spec 中明确 Bootstrap 是 runtime/ 层的「顶层编排器」，Assembly 只负责 Cell 子集，Bootstrap 负责 Assembly + HTTP + Worker + Config 的完整生命周期
  3. 建议在 Dependencies.Config 中注入 runtime/config 加载的配置，否则 Cell.Init 无法获取运行时配置

### KG-6: In-process EventBus 需要定义 kernel 级接口

- **严重度**: 中
- **类别**: 核心约束
- **问题**: spec 5.3 节描述 "in-process event bus（内存实现）" 作为 Phase 2 的事件通信方式，Phase 3 替换为 RabbitMQ。但当前 kernel/ 中只有 `outbox.Publisher` 接口（单向发布），没有 Subscriber 接口。Cell 间事件订阅所需的 `Subscribe(topic string, handler func(Entry)) error` 模式在 kernel/ 中不存在。
  1. `outbox.Publisher.Publish(ctx, topic, payload)` 是 fire-and-forget 的发布端
  2. 消费端（subscribe）无 kernel 级接口
  3. in-process EventBus 如果只在 runtime/ 中定义，则 cells/ 需要 import runtime/ 才能订阅事件——但 CLAUDE.md 允许 cells/ 依赖 runtime/
- **建议**: 
  1. 在 kernel/ 中新增 `Subscriber` 接口（与 Watermill message.Subscriber 对标），使事件消费成为 kernel 级契约
  2. 或在 runtime/ 中定义 EventBus 接口（包含 Publish + Subscribe），cells/ 通过 runtime/ 使用
  3. 无论哪种方案，需在 spec 中明确接口定义位置和依赖路径

### KG-7: product-context S8 与 spec NFR-2 外部依赖白名单不一致

- **严重度**: 中
- **类别**: 元数据合规
- **问题**: product-context.md 的 S8 声明 "Phase 2 新增外部依赖仅 go-chi/chi/v5 和 golang.org/x/crypto，不引入其他第三方库"。但 spec NFR-2 的白名单包含 5 个依赖：go-chi/chi/v5、golang.org/x/crypto、fsnotify/fsnotify、prometheus/client_golang、go.opentelemetry.io/otel。二者矛盾。
  1. 如果按 S8 执行，Prometheus 指标（FR-5.1）和 OTel tracing（FR-5.2）无法实现
  2. 配置热更新的 fsnotify watcher（FR-2.2）也无法实现
- **建议**: 
  1. 以 spec NFR-2 的 5 依赖白名单为准，同步修正 product-context S8 措辞
  2. 或者将 Prometheus/OTel/fsnotify 标记为可选依赖（build tag 控制），使 S8 在最小模式下成立

### KG-8: session-validate 和 authorization-decide/rbac-check 未声明 HTTP contract

- **严重度**: 低
- **类别**: 契约完整性
- **问题**: spec FR-8 描述 session-validate 提供 "Validate access token -> 返回 Claims"，authorization-decide 提供 "Evaluate(subject, resource, action) -> Allow/Deny"，rbac-check 提供 "HasRole / ListRoles"。这些操作在运行时都需要被外部 Cell 或 HTTP 端点调用，但三个 slice 的 contractUsages 均为空。如果它们只在 access-core 内部使用（同 Cell 内 slice 间调用），无需 contract；但如果其他 Cell 需要调用（如 audit-core 需要验证操作者身份），则需要声明 HTTP 或 command contract。
- **建议**: 
  1. 评估 session-validate / authorization-decide / rbac-check 是否仅限 Cell 内部使用
  2. 如果跨 Cell 使用，补充对应的 HTTP contract 和 slice contractUsage 声明
  3. spec 应明确这些 slice 的调用边界

### KG-9: J-config-hot-reload journey 引用 audit-core 但无对应 contract

- **严重度**: 低
- **类别**: 元数据合规
- **问题**: J-config-hot-reload.yaml 的 cells 列表包含 audit-core，但其 contracts 仅列出 event.config.changed.v1。event.config.changed.v1 的 subscribers 不包含 audit-core（仅 access-core）。如果 audit-core 不参与此 journey 的任何 contract 交互，将其列在 cells 中违反最小依赖原则，且 governance ADV 规则可能报 warning。
- **建议**: 
  1. 如果配置热更新确实需要审计记录，则在 event.config.changed.v1 的 subscribers 中加入 audit-core
  2. 否则从 J-config-hot-reload.yaml 的 cells 中移除 audit-core

---

## (b) 集成风险评估

| 风险项 | 级别 | 说明 | 缓解措施 |
|--------|------|------|---------|
| 事件订阅接口空白 | 高 | kernel/ 仅有 outbox.Publisher，无 Subscriber 接口。cells/ 的事件消费无法通过标准 kernel 接口实现，可能导致各 Cell 自行实现不一致的订阅机制 | 在 Wave 1 开始前确定 Subscriber 接口定义位置（kernel/ 或 runtime/），并先行实现 in-process EventBus |
| Slice 依赖注入模式未确定 | 高 | Slice.Init(ctx) 不接受依赖参数，16 个 slice 的 service 层需要 repository port 注入。如果实施中各 Cell 采用不同的注入模式，后续重构成本极高 | 在 Wave 3 开始前，由一个 Cell（如 config-core，slice 最少）先行验证注入模式，形成参考实现 |
| Assembly 与 Bootstrap 职责重叠 | 中 | Assembly.Start 有自己的 Init+Start 编排，Bootstrap 也有完整的启动流程。二者的错误处理和回滚逻辑可能冲突 | 明确 Bootstrap 是 Assembly 的使用者，Assembly.Start 由 Bootstrap 统一调用，Bootstrap 负责外层回滚 |
| YAML 元数据与 Go 代码一致性 | 中 | 16 个 slice 的 slice.yaml contractUsages 存在大量遗漏（KG-1/KG-2/KG-3/KG-8），Phase 2 Go 实现可能与元数据声明不一致，导致 gocell validate 持续报错 | 在 Wave 3 开始前完成所有 slice.yaml 和 contract.yaml 的修正，确保 gocell validate 零 error |
| runtime/ 依赖 kernel/ 的合规性 | 低 | CLAUDE.md 规定 "runtime/ 不依赖 cells/、adapters/" 但未明确 runtime/ 是否可依赖 kernel/。runtime/http/health 需要 kernel/cell.HealthStatus 类型 | 在 CLAUDE.md 中显式添加 "runtime/ 可依赖 kernel/ 和 pkg/"，或在 runtime/ 中重新定义 HealthStatus 接口 |
| 外部依赖数量分歧 | 低 | product-context S8 和 spec NFR-2 对外部依赖数量描述矛盾 | 统一为 NFR-2 的 5 依赖白名单 |

---

## (c) 本 Phase 必须验证的内核约束清单

### 分层隔离

- [ ] C-01: kernel/ 不新增对 runtime/ 的 import（go build 验证 + depcheck）
- [ ] C-02: runtime/ 不 import cells/ 或 adapters/（go build 验证 + depcheck）
- [ ] C-03: cells/ 不 import adapters/（go build 验证 + depcheck）
- [ ] C-04: cells/ 之间不直接 import 另一个 cell 的 internal/（depcheck 自定义规则）

### Cell 生命周期

- [ ] C-05: 每个 Cell 的 BaseCell 状态机正确运转：New -> Init -> Start -> Health=healthy -> Stop -> Health=unhealthy
- [ ] C-06: Assembly.Start 失败时 LIFO 回滚已启动的 Cell
- [ ] C-07: Assembly.Stop 尽力而为，不因单个 Cell 失败中止
- [ ] C-08: Cell.Init 可重入（stopped 状态可再次 Init），支持热重启场景

### 元数据合规

- [ ] C-09: 所有 cell.yaml 必填字段完整：id / type / consistencyLevel / owner / schema.primary / verify.smoke
- [ ] C-10: 所有 slice.yaml 必填字段完整：id / belongsToCell / contractUsages / verify.unit / verify.contract
- [ ] C-11: 禁止使用旧字段名（cellId / sliceId / contractId / assemblyId / ownedSlices 等）
- [ ] C-12: cell.yaml 不包含 slices / journeys / contracts 反向索引
- [ ] C-13: 动态交付状态（readiness / risk / blocker 等）仅出现在 status-board.yaml

### 契约完整性

- [ ] C-14: 每个 contract.yaml 的 ownerCell 引用存在的 cell（REF-03）
- [ ] C-15: 每个 slice contractUsage 的 role 对 contract kind 合法（TOPO-01）
- [ ] C-16: provider-role slice 的 belongsToCell 匹配 contract provider（TOPO-02）
- [ ] C-17: consumer-role slice 的 belongsToCell 出现在 contract consumers 中（TOPO-03）
- [ ] C-18: contract.consistencyLevel 不超过 ownerCell 的 consistencyLevel（TOPO-04）
- [ ] C-19: 每个 event contract 有且仅有一个 publisher Cell，subscriber 列表与 slice 声明匹配
- [ ] C-20: http contract 的 server/clients 与 slice 的 serve/call 角色一一对应

### 一致性等级

- [ ] C-21: L0 Cell 不出现在任何 contract 的 endpoints 中（TOPO-05）
- [ ] C-22: L2 级别的 slice 必须使用 outbox 模式发布事件（代码审查）
- [ ] C-23: L3 跨 Cell 最终一致场景（audit-core 消费 access-core 事件）正确声明一致性等级

### 验证闭包

- [ ] C-24: 每个 slice 的 verify.unit 和 verify.contract 列表非空（对有 contractUsage 的 slice）
- [ ] C-25: 每个 journey 的 auto passCriteria 有对应 checkRef
- [ ] C-26: gocell validate 对完整项目元数据零 error

### 错误处理

- [ ] C-27: 所有对外错误使用 pkg/errcode，禁止裸 errors.New
- [ ] C-28: domain 层不返回 HTTP 状态码
- [ ] C-29: Cell 生命周期错误使用 errcode.ErrLifecycleInvalid

---

## (d) 工作流可执行性评估

### 8 阶段总览

| 阶段 | 名称 | 可执行性 | 风险 |
|------|------|---------|------|
| Stage 0 | Init | 可执行 | 低 |
| Stage 1 | Specify | 已完成 | — |
| Stage 2 | Review | 当前阶段 | 中 |
| Stage 3 | Decide | 可执行 | 中 |
| Stage 4 | Plan | 可执行 | 中 |
| Stage 5 | Implement | 有卡点 | 高 |
| Stage 6 | Review-Fix | 可执行 | 中 |
| Stage 7 | QA | 有卡点 | 高 |
| Stage 8 | Close | 可执行 | 低 |

### 可能卡住的环节

#### Stage 3 (Decide) — 必须前置解决的架构决策

以下 3 个决策如果不在 Stage 3 明确裁定，将在 Stage 5 产生大量返工：

1. **Subscriber 接口定义位置**：kernel/ 还是 runtime/？这决定了 cells/ 的 import 路径和 in-process EventBus 的实现位置。建议在 Stage 3 产出一个 ADR（Architecture Decision Record）。

2. **Slice 依赖注入模式**：构造时注入 vs Init 时注入？16 个 slice 全部受影响。建议在 Stage 3 选定方案后写一个 reference slice 实现。

3. **runtime/ 依赖 kernel/ 的合规声明**：CLAUDE.md 需要新增一条明确规则。

#### Stage 5 (Implement) — 并行化瓶颈

1. **Wave 1 与 Wave 3 的实际串行依赖比预期更深**：spec 将 Wave 1（runtime/）与 Wave 3（cells/）设为串行，但 cells/ 的 Go 实现需要 runtime/auth（JWT 签发/验证）和 runtime/http（路由注册）。如果 Wave 1 的 auth 或 http 模块交付延迟，Wave 3 全部阻塞。建议在 Wave 1 优先交付 auth 和 http/router 的接口定义（interface-first），cells/ 可基于接口 mock 并行开发。

2. **16 个 slice + 7 个中间件 + 6 个 runtime 模块 = 约 80 个文件**：单人实施难以在合理时间内完成。spec 建议 Wave 3 的 3 个 Cell 并行开发，但如果只有一个实施者（Claude），实际为串行。建议按 Cell 重要性排序：access-core > config-core > audit-core，优先保证 SSO 登录 journey 可端到端验证。

3. **元数据修正必须前于代码实现**：KG-1/KG-2/KG-3 识别的 slice.yaml 和 contract.yaml 遗漏如果不在 Stage 5 开始前修正，gocell validate 会持续报错，阻碍每个 batch 的 gate 检查。建议在 Stage 4 (Plan) 中安排一个 "metadata alignment" 批次作为 Wave 0。

#### Stage 7 (QA) — 端到端验证卡点

1. **J-audit-login-trail 是跨 Cell journey**：需要 access-core 发布 event.session.created.v1，audit-core 消费并写入 hash chain，audit-core 发布 event.audit.integrity-verified.v1。如果 KG-1（audit-core 缺 subscribe 声明）和 KG-6（无 Subscriber 接口）未解决，此 journey 不可能 PASS。这是 8 条 journey 中风险最高的一条。

2. **J-config-hot-reload 和 J-config-rollback** 同样依赖事件订阅机制。config-subscribe slice 当前 contractUsages 为空（KG-2），且依赖 in-process EventBus 的实现。

3. **gocell verify journey 命令的实现**：当前 cmd/gocell 的 verify 子命令需要能执行 journey 级端到端测试。spec Gate 验证章节列出了 8 条 verify journey 命令，但这些命令的底层实现（如何触发 HTTP 请求、验证事件流转、检查审计记录）在 Phase 2 spec 中未详细说明。如果 verify journey 仅检查元数据一致性而非运行时行为，则可执行；如果需要实际启动 assembly 并执行 HTTP 调用，则需要 adapter（Phase 3）或 in-memory mock。

4. **覆盖率门禁**：runtime/ >= 80% 和 cells/ >= 80% 是硬性要求。如果 Cell 的 service 层大量依赖 repository port 接口（Phase 3 才有实现），测试需要 mock 全部 port，mock 代码量可能接近业务代码量。建议在 Stage 4 评估 mock 生成工具（如 mockgen）是否纳入开发依赖。

### 总体评估

spec 的 8 阶段工作流**可以走完**，但有 **3 个高风险卡点**需要在 Stage 3 (Decide) 前置解决：

1. 事件订阅接口定义（KG-6）
2. Slice 依赖注入模式（KG-4）
3. YAML 元数据缺口修正（KG-1/KG-2/KG-3）

如果这 3 个问题不在 Stage 3 决策并在 Stage 4 Plan 中安排修正批次，Stage 5 实施将频繁受阻，Stage 7 的跨 Cell journey 验证大概率失败。
