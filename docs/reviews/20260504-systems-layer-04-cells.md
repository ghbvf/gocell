# GoCell 系统工程逐层审查 — cells/ 层

| 维度 | 值 |
|------|---|
| 审查日期 | 2026-05-04 |
| Commit | 11600a4f (`docs(roadmap+backlog): cut #11 VISUALIZE-CMD ...`) |
| 选定维度 | ② 职责与内聚 / ③ 依赖（仅契约） / ④ 状态与生命周期 / ⑨ 可演进性 |
| 审查范围 | `cells/accesscore` `cells/auditcore` `cells/configcore` `cells/internal` |

## 0. 摘要

cells/ 层的形态层骨架可信：3 个 core cell 都通过 `kernel/cell.BaseCell` + `reg.RouteGroup` + `reg.Subscribe(spec, handler, consumerGroup)` 模式装配；横向依赖确实只穿过 contract（要么是 event 订阅，要么是 `http.config.internal.get.v1` 内部 HTTP），未发现兄弟 cell internal/ 的直接 import；adapters 通过 cell 内 `internal/ports` 接口注入，顶层 `adapters/` 没有被业务直接 import。

但层内一致性偏弱，三个 cell 在以下方面已经出现"约定漂移"：cell.yaml 与 `NewBaseCell(CellMetadata{...})` 字面量在 owner/schema/smoke/consistencyLevel 四处不一致；slice 划分上单 slice 揽多 verb（`auditappend` 一拖十三）；`configcore/cell_routes.go` 退化为占位文件；`accesscore/configreceive` 显式自承"placeholder per ADV-05"；`configsubscribe.Cache` 无界增长、未挂 Lifecycle。这些问题对当前 3 cell 还可控，但 scaffold 出第 4 / 第 N 个 cell 时会被复制放大。

## 1. 评级表

| 维度 | 评级 | 一句话理由 |
|------|------|-----------|
| ② 职责与内聚 | ⚠️ 部分具备 | handler/service 命名分层到位，但 `auditappend` / `configread` 单 slice 揽多 verb；`cell_routes.go` 退化占位；`internal/` 子包数量在 3 cell 间高度不对称 |
| ③ 依赖（仅契约） | ✅ 已具备 | cells 之间无 internal/ 互 import，跨 cell 通过 event 订阅或 `http.config.internal.get.v1` 走 contract；adapters 通过 cell 内 ports 接口注入，无顶层 `adapters/` 直 import |
| ④ 状态与生命周期 | ⚠️ 部分具备 | Init 三 cell 都完整且 fail-fast；但 BaseCell 无显式 Start/Stop，`reg.Lifecycle` hook 仅 accesscore 使用；`configsubscribe.Cache` 进程内无界增长，无 Stop 钩子归属 |
| ⑨ 可演进性 | ⚠️ 部分具备 | `gocell scaffold cell/slice` 命令存在；但 cell.yaml ↔ Go metadata 字面量四处漂移、3 cell Init 切分粒度不同、`l0Dependencies: []` 全空、placeholder subscriber 注释承认占位 — 标准动作未沉淀为唯一来源 |

## 2. 问题清单

#### [P0] cell.yaml 与 NewBaseCell metadata 字面量四处漂移
- **维度**：⑨ 可演进性
- **位置**：`cells/accesscore/cell.yaml:5-12` vs `cells/accesscore/cell.go:280-282`；`cells/auditcore/cell.yaml:5-12` vs `cells/auditcore/cell.go:199-201`；`cells/configcore/cell.yaml:10-17` vs `cells/configcore/cell.go:158-160`
- **复杂度**：Cx2
- **现象**：accesscore 的 yaml 写 `Owner.Role: cell-owner` / `Schema.Primary: cell_access_core` / `Smoke: smoke.accesscore.startup`，Go 字面量写 `access-owner` / `users` / `accesscore/smoke`；auditcore、configcore 同形态。configcore 的 cell.yaml 还**完全省略** `consistencyLevel` 和 `durabilityMode` 顶层字段（CLAUDE.md 明确要求 cell.yaml 必填 consistencyLevel）。这是双真理源——yaml 是治理 SoR，但代码运行时用 NewBaseCell 字面量，governance 校验过的元数据不进入运行时。
- **建议方向**：让 NewXxxCore 接受外部加载的 `cell.CellMetadata`（来自 yaml），或让 `cell.NewBaseCell` 通过 `//go:embed cell.yaml` 解析自填；配 archtest 守卫两侧字段相等。

#### [P1] 单 slice 多 verb：auditappend / configread 违反"单 slice 单 verb"
- **维度**：② 职责与内聚
- **位置**：`cells/auditcore/slices/auditappend/slice.yaml:3-31`；`cells/configcore/slices/configread/slice.yaml:3-9`
- **复杂度**：Cx3
- **现象**：`auditappend` 一个 slice 同时订阅 13 个跨 cell 事件 + publish 1 个，contractUsages 共 14 项；`configread` 一个 slice 同时 serve `http.config.get.v1` / `http.config.list.v1` / `http.config.internal.get.v1`，跨 PrimaryListener 与 InternalListener 两个端口面。两者都让"slice = 内聚动词"的边界稀释成"按聚合根做一勺"。
- **建议方向**：`auditappend` 按事件域拆 `auditappend-session` / `auditappend-user` / `auditappend-config` / `auditappend-role`（共享 service.HandleEvent 的 dispatch 不变，只是 slice.yaml 各自承担 verify.contract）；`configread` 把 `internal.get` 拆出 `configread-internal` slice，让两个 listener 的 contract 责任在 yaml 上正交。

#### [P1] occupier subscriber：configreceive 自承占位违反 ADV-05 精神
- **维度**：⑨ 可演进性 / ② 职责与内聚
- **位置**：`cells/accesscore/slices/configreceive/service.go:27-30`
- **复杂度**：Cx2
- **现象**：service 注释自述"HandleEntryUpserted/HandleEntryDeleted are currently observability-only (logs only) ... the current subscription is a placeholder per ADV-05 (active event must have subscribers)"。也就是这个 slice 之所以存在，是为了让 ADV-05 治理规则不报"active event 无 subscriber"——本应走 `lifecycle: draft` 的事件被强行钉了一个空 consumer。等于绕过治理。
- **建议方向**：要么让 configcore 的 entry-upserted/deleted contract 标 `lifecycle: draft` 直到 accesscore 真有消费动机；要么把 configreceive 提到 cell.yaml `l0Dependencies` 共享 cache。当前形态助长"造个 placeholder 让规则闭环"。

#### [P1] configsubscribe.Cache 无界 + 未挂 Lifecycle
- **维度**：④ 状态与生命周期
- **位置**：`cells/configcore/slices/configsubscribe/service.go:33-46`、`97-107`
- **复杂度**：Cx2
- **现象**：注释自承"tombstone entries are retained for the lifetime of the process so that the monotonic protection holds across replays. If process memory becomes a concern ... a TTL-based eviction or persistent tombstone store should be introduced — that is out of scope for this PR"。该 Cache 由 `NewService()` 在 Init 阶段构造，但既不暴露给 `reg.Lifecycle`（无 OnStop 清理 / 持久化 snapshot），也没有内存上限，长寿进程会无界。configcore Init() 也没注册任何 lifecycle hook（对比 accesscore.refreshGCHook + initialAdmin.Hook）。
- **建议方向**：要么让 Cache 走 `kernel/cell.LifecycleHook` 在 OnStart 时 hydrate / OnStop snapshot；要么改成 LRU + size cap 并把 capacity 暴露成 cell option；最差也要在 Cache 上挂个 metric 让运维看到增长曲线。

#### [P1] identitymanage 标记 L1 但 publish 5 个 user.* L2 事件
- **维度**：② 职责与内聚 / ④ 状态与生命周期
- **位置**：`cells/accesscore/cell_init.go:195`（`cell.NewBaseSlice("identitymanage", "accesscore", cell.L1)`）
- **复杂度**：Cx1
- **现象**：identitymanage 注入 emitter（`identityOpts := []identitymanage.Option{identitymanage.WithEmitter(c.emitter), ...}`），且 auditcore 订阅了 `event.user.created.v1` / `user.locked.v1` / `user.updated.v1` / `user.deleted.v1` / `user.unlocked.v1` 五个事件——只能由 identitymanage 产生。一个 publish event 的 slice 必须是 L2 (OutboxFact)，不是 L1。
- **建议方向**：`AddSlice(... cell.L2)`；同时检查其他 8 处 `cell.NewBaseSlice` 的 L 字面量是否反映真实的 contractUsages 角色。

#### [P2] 3 cell 的 Init 切分粒度不一致（约定漂移）
- **维度**：⑨ 可演进性
- **位置**：`cells/accesscore/cell_init.go:294-432`（7 个 helper）；`cells/auditcore/cell.go:211-274`（5 个 helper）；`cells/configcore/cell_init.go:36-167`（不同切分：initAllSlices/registerRouteGroups/registerSubscriptions + 6 个 init*Slice）
- **复杂度**：Cx2
- **现象**：三个 cell 的 Init 解构思路各异——accesscore 按生命周期阶段（validate→slices→bind→routes→subs→health），auditcore 按资源类型（emitter→hmac→slices→cursorcodec→query→routes），configcore 按 slice 一对一（initWriteSlice/initReadSlice/...）。第 4 个 cell 的作者无法从 3 个先例中提炼唯一标准。
- **建议方向**：在 `kernel/cell` 提供 `BaseCell.RegisterStandard(reg, StandardInit{Slices, RouteGroups, Subscriptions, Health, Lifecycle})` 模板，让每个 cell 的 Init 体收敛到声明数据；scaffold 直接产出该结构。

#### [P2] cells/configcore/cell_routes.go 退化为占位文件
- **维度**：⑨ 可演进性
- **位置**：`cells/configcore/cell_routes.go:1-5`
- **复杂度**：Cx1
- **现象**：整个文件只有"intentionally empty after the Batch 3 migration ... retained as a placeholder to record the migration context"——空 Go 文件。CLAUDE.md 写"项目无外部消费方，不考虑向后兼容"，没有任何理由保留迁移痕迹空壳。
- **建议方向**：直接删除文件；迁移上下文搬到 `docs/architecture/` 或 commit message。

#### [P2] cell 内 internal/ 子包形态在 3 cell 间高度不对称
- **维度**：② 职责与内聚 / ⑨ 可演进性
- **位置**：`cells/accesscore/internal/{adapters,adminprovision,dto,domain,events,mem,ports,sessionmint}` vs `cells/auditcore/internal/{mem,ports}` vs `cells/configcore/internal/{events,mem,ports}`
- **复杂度**：Cx2
- **现象**：accesscore 有 8 个 internal/ 子包（含 `internal/adapters/http/`、`internal/sessionmint/`、`internal/adminprovision/`、`internal/dto/`、`internal/domain/`），auditcore 只有 2 个（`mem`/`ports`），configcore 3 个。CLAUDE.md cell-patterns.md 规定 DTO 三档（A/B/C），但 3 cell 对 B 档（`internal/dto/`、`internal/domain/`、`internal/events/`）的使用完全不对称——auditcore 没有 dto/domain/events，configcore 没有 dto/domain，accesscore 全有。这不是业务复杂度差异，而是同一 cell 模型在 3 个实现中被不同程度地"完整化"。
- **建议方向**：scaffold cell 模板预生成 `internal/{ports,domain,dto,events,mem}` 五个空目录 + README，让所有 cell 以同样起点生长；或在 docs 明确记录"哪些 internal/ 子包是 cell 必备 vs 可选"。

#### [P2] l0Dependencies: [] 在 3 cell 全空，L0 共享路径未被使用
- **维度**：⑨ 可演进性
- **位置**：`cells/accesscore/cell.yaml:13`、`cells/auditcore/cell.yaml:13`、`cells/configcore/cell.yaml:18`
- **复杂度**：Cx1
- **现象**：CLAUDE.md "Cell 之间只通过 contract 通信；L0 Cell（纯计算库）可被同一 assembly 内的兄弟 Cell 直接 import"。但 3 cell 都是 `l0Dependencies: []`，且 cells 目录下没有任何 `type: l0` 的 cell。L0 通道只是 schema 字段，无实例、无验证。当前 `pkg/query.CursorCodec`、`runtime/auth/refresh` 这类共享逻辑实际上在 runtime/ 而非 L0 cell——意味着 L0 Cell 概念在形态层是死代码路径。
- **建议方向**：要么把当前 `cells/internal/testoutbox` 之类纯计算工具升级为示例 L0 cell 验证通路；要么在文档明确标注 L0 cell 是"未来扩展点，当前无实例"，避免 scaffold 命令承诺一个不存在的能力。

## 3. 跨层观察

**cells ↔ contracts**：跨 cell 通信 100% 走 contract，包括 event（auditcore/auditappend 订阅 13 个）和 HTTP（accesscore/configreceive 通过 `http.config.internal.get.v1` 调 configcore）。`accesscore/internal/adapters/http/configclient.go` 是 cell 内的 HTTP client adapter，import 的是 `kernel`、`pkg`、`runtime/auth`，没有 import `cells/configcore` —— 这是 contract 通信的正确实现。但 `sessionlogin/slice.yaml:8-9` 声明 `http.config.get.v1 role: call` 配 `waivers: 集成测试已覆盖 expiresAt: 2026-06-01`，而 `service.go` 没有 configcore 调用代码 —— slice.yaml 的 contractUsages 与代码漂移；FMT-18 / VERIFY-01 是否 catch 取决于 verify.contract 是否真有静态 trace。

**cells ↔ runtime 注入**：cells 通过 `runtime/auth.JWTIssuer/Verifier`、`runtime/auth/refresh.Store`、`runtime/observability/metrics` 接入运行时设施，所有依赖都是 Option 注入（不在 cell 包内 new），这点 3 cell 一致。但 cells 没有显式定义"实现什么 runtime 接口"的契约 —— `cell.HealthProber` 是 emitter 的可选接口，由 cells 检测后转发给 `reg.Health()`，这是 fail-soft 模式（`if hc, ok := c.emitter.(cell.HealthProber); ok`），3 cell 重复 4 次，可抽 `cell.RegisterEmitterHealthProbes(reg, emitter)` helper。

**cells ↔ assembly**：3 cell 都通过 `cmd/corebundle` 的 `*_module.go` 装配，CellModule 接口稳定。但 `assemblies/corebundle/assembly.yaml` 里 cell 列表与 `cmd/corebundle` modules 各自维护，两边一致性靠人工。

## 4. 一句话结论

cells 形态骨架成立、跨 cell 隔离干净，但 cell.yaml ↔ Go metadata 漂移、单 slice 多 verb、placeholder subscriber、Init 切分粒度三套打法，已经显著拖累"加第 4 个 cell"的边际成本——优先把 cell.yaml 升格为唯一元数据来源 + 收敛 Init 模板，再处理 slice 内聚和 Cache 生命周期。
