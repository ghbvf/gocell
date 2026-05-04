# GoCell 系统工程逐层审查 — journeys/ 层

| 项目 | 内容 |
|------|------|
| 审查日期 | 2026-05-04 |
| 基线 commit | 11600a4f |
| 审查范围 | `/Users/shengming/Documents/code/gocell/journeys/` 8 条 J-*.yaml + status-board.yaml + 关联 `kernel/verify/` |
| 选定维度 | ① 边界（验收）/ ⑦ 可测试性 / ⑨ 可演进性 / ⑩ 第一性原理推导 |
| 形态层口径 | 仅审 schema/闭环/可推性，不审 goal 文字业务正确性 |

## 0. 摘要

journeys/ 层以 8 条 J-*.yaml 编码端到端验收边界，配合 `kernel/verify/runner.go` + `ref.go` 的 4 类 prefix（journey / smoke / unit / contract）形成 metadata 驱动的 CI 闭环，整体 **schema 简洁、可推导性较强**。但存在三类系统级缺口：

1. **fixtures/ 目录仅有 .gitkeep，CLAUDE.md 声称"供 run-journey 使用"但 schema 无任何 `fixtures` 字段**——文档与实现脱节，journey 实际是"代码硬编码 + 命名约定"驱动，不是"数据驱动"。
2. **status-board.yaml 与 J-*.yaml.lifecycle 双轨表达状态**——`runner.go:202` 用 `j.Lifecycle != "active"` 过滤，所有 8 条 yaml 均为 `experimental`，而 status-board 用 `state: doing/todo`；两套状态机无强一致性约束，存在漂移风险。
3. **journey 与 contract/cell 关系仅靠数组列表声明，没有 actor 维度**——actors.yaml 有 4 个外部 actor（edge-bff / external-audit-sink / 等），但 journey 不绑定 actor；从 journey 不可推出"谁触发"，与"用户场景式"切分意图相悖。

passCriteria.checkRef 命名规则（`journey.{journeyID}.{suffix}`）通过 `kernel/verify/ref.go:52` 编译为 `^Test{CamelCase(journeyID)}{CamelCase(suffix)}$` 强匹配，加上 `runner.go:215` 对 active journey 强制要求至少一个 auto checkRef，闭环约束相对扎实。

## 1. 评级表

| 维度 | 评级 | 说明 |
|------|------|------|
| ① 边界（验收） | ⚠️ 部分具备 | passCriteria + checkRef 闭环清晰；但 schema 缺 actor / triggers / preconditions，"谁来做"不可推 |
| ⑦ 可测试性 | ⚠️ 部分具备 | runner 强制 auto refs + ZeroMatch 检查（runner.go:310）；fixtures/ 仅占位、active lifecycle 未启用导致 `RunActiveJourneys` 实际跳过全部 8 条 |
| ⑨ 可演进性 | ⚠️ 部分具备 | 8 条 yaml 字段一致；但 lifecycle vs status-board.state 双轨、加新 journey 无 schema 校验文件、J-ordercreate 占位条目无对应 yaml |
| ⑩ 第一性原理推导 | ⚠️ 部分具备 | journey 与 contract/cell 关系唯一可推；但"按用户场景切"与"按系统能力切"未约束，actor 维度缺失暗示 schema 未收敛 |

## 2. 问题清单

#### [P0] active journey 全部为 experimental，CI `verify journey --active` 实际零执行
- **维度**：⑦ 可测试性
- **位置**：`journeys/J-ssologin.yaml:3` 等 8 条 + `kernel/verify/runner.go:202`
- **复杂度**：Cx2
- **现象**：所有 8 条 J-*.yaml 的 `lifecycle: experimental`，`runner.go:202` 用 `if j.Lifecycle != "active" { continue }` 过滤；意味着 `gocell verify journey --active` 在当前仓库会**静默跳过全部 journey**，不报错。配合 status-board.yaml 中 J-ssologin 已 `state: doing`，存在"看似有 8 条验收红线，实际 CI 零拦截"的悖论。
- **建议方向**：要么把至少 J-ssologin 升 `lifecycle: active` 让闭环生效，要么 `RunActiveJourneys` 在 active 集合为空时输出 warn-but-fail 提示。

#### [P0] passCriteria 引用了未声明的 contract（J-confighotreload event.config.entry-deleted.v1）
- **维度**：① 边界（验收）
- **位置**：`journeys/J-confighotreload.yaml:13,15`
- **复杂度**：Cx1
- **现象**：`contracts:` 列出 `event.config.entry-upserted.v1` 与 `event.config.entry-deleted.v1`，passCriteria 文本也提到"config.entry-upserted / config.entry-deleted 状态同步事件"。需要核验 `contracts/event/config/` 下是否真有 entry-deleted v1 contract 文件。若无，则 journey 引用了不存在的 contract，FMT/ADV 校验应当拦截但目前未见此规则。[需确认 contracts/event/config/ 实际清单]
- **建议方向**：在 `gocell validate` 增加 journey.contracts ↔ contracts/ 双向存在性校验（类似 ADV-06 对 contractUsages 的处理）。

#### [P1] fixtures/ 仅 .gitkeep + schema 无 fixtures 字段，文档/实现脱节
- **维度**：⑦ 可测试性
- **位置**：`fixtures/.gitkeep`、所有 J-*.yaml、`CLAUDE.md` 第 13 行
- **复杂度**：Cx3
- **现象**：CLAUDE.md 声明 `fixtures/` "供 run-journey 使用"，但 8 条 yaml 无任何 `fixtures:` / `dataset:` / `seed:` 字段，runner.go 也未读取 fixtures 目录。journey 数据完全来自 Test 函数内部硬编码。"数据驱动 journey"是文档承诺，实现层未兑现。
- **建议方向**：二选一——(a) 删除 fixtures/ 与 CLAUDE.md 引用，承认 journey 是代码硬编码；(b) 引入 `fixtures: [fixture-id]` 字段 + runner 注入机制。**当前混杂状态最差**。

#### [P1] status-board.yaml 与 lifecycle 双轨状态，无一致性约束
- **维度**：⑨ 可演进性
- **位置**：`journeys/status-board.yaml:1-46`
- **复杂度**：Cx2
- **现象**：J-ssologin 在 board 中 `state: doing`，yaml 中 `lifecycle: experimental`；其他 7 条 board 全 `todo`、yaml 全 `experimental`。两套状态机各表，没有约束哪个状态对应哪个 lifecycle，也没有 `gocell validate` 校验它们的转换矩阵。新人不知道何时该改 board、何时该改 lifecycle。
- **建议方向**：定义状态机 `todo→doing→done` ↔ `experimental→active→stable` 的强映射，validate 时双向校验；或合并为单一字段。

#### [P1] J-ordercreate 在 status-board 占位但无对应 yaml，schema 漂移
- **维度**：⑨ 可演进性
- **位置**：`journeys/status-board.yaml:41-45`
- **复杂度**：Cx1
- **现象**：board 第 41 行 `journeyId: J-ordercreate` 标 `state: todo`，但 `journeys/` 目录无 `J-ordercreate.yaml`。`runner.go:146` `RunJourney` 会返回 `ErrJourneyNotFound`，但 `RunActiveJourneys` 从 `r.project.Journeys` 取，根本不感知 board 中的占位条目。board 沦为"承诺-only 文档"。
- **建议方向**：validate 时校验 `status-board.journeyId ⊆ journeys/J-*.yaml`，未落地的占位用单独 `roadmap.yaml` 承载，避免 board 真假混杂。

#### [P2] passCriteria.text 与 checkRef 强耦合但无对齐校验
- **维度**：① 边界（验收）
- **位置**：所有 J-*.yaml `passCriteria` 节
- **复杂度**：Cx2
- **现象**：例如 J-sessionlogout `text: Session 标记为已吊销` / `checkRef: journey.J-sessionlogout.session-revoke`，文字与 suffix 是平行的双源真相。改 text 不强制改 checkRef。`ref.go:52` 仅校验语法，不校验语义。批量重命名/拆分时易漂移。
- **建议方向**：在 passCriteria 中合并 text 与 ref（保留单一标识符 + i18n 文案表），或 validate 时人工锁定二者对应表。

## 3. 第 10 维度专项推导：journey 与 contract/cell 的必然性论证

**反证一：拿掉 journey 层会丢失什么？**

如果删掉 journeys/ 仅保留 contract.endpoints + cell.verify.smoke：
- 失去 **跨 cell 跨 contract 的"组合"语义**——例如 J-auditlogintrail 同时引用 accesscore + auditcore 两个 cell + `event.session.created.v1` + `event.audit.integrity-verified.v1` 两个 contract 编码"事件被消费 + hash chain 追加 + 完整性验证"的串行闭环；contract.endpoints 只能描述"谁 publish/subscribe"，无法表达 **三步串行的端到端业务断言**。
- 失去 **manual 验收挂载点**——J-ssologin 第一条 `mode: manual / text: OIDC 重定向完成` 是 Phase 3 OIDC 适配器就绪后的人工检查项，contract / smoke 层无此抽象。

**结论：journey 层不是冗余补丁，删除会丢"组合验收 + 人工验收"两类不可下推语义。**

**反证二：journey 是否应按"用户场景"切？**

当前 8 条全是用户场景（登录/刷新/登出/锁定/审计/热更新/回滚/创建用户）。但 J-confighotreload + J-configrollback 在 cells 列表上几乎重叠（configcore + accesscore + auditcore），contracts 上也共享 `event.config.entry-upserted.v1`。从"系统能力"角度它们是同一组（"配置版本切换 + 订阅同步"）。"用户场景式"切分导致 journey 间存在 **隐藏依赖**——回滚必然先有 hot-reload 通路，但 yaml 不表达这种 prerequisite。

**结论：当前 journey 缺 `dependsOn: [J-...]` 字段；若坚持用户场景切，需补依赖图；若改系统能力切，需重组。这是"任意性切分"的标志——schema 未收敛。**

**反证三：actor 维度缺失暗示什么？**

actors.yaml 注册 4 个外部 actor（edge-bff / external-audit-sink / 等），journey 不绑定 actor。J-ssologin 的 goal 是"用户完成 SSO 登录"，"用户"这个角色既不在 cells 也不在 actors.yaml 显式表达。从 journey 不可推出"是 edge-bff 触发 http.auth.login.v1 还是用户直连"。

**结论：journey schema 缺 `actors: [actor-id]` 字段属于冗余补丁信号——目前用 goal 文本承载该信息，无法静态校验。建议补 actors 字段且 validate 时校验 ⊆ actors.yaml ∪ {`internal-user`}。**

## 4. 跨层观察

- **journeys ↔ contracts**：单向引用（journey.contracts → contracts/），无反向 contract.referencedBy。新增 contract 时无法静态发现"哪些 journey 该升级 v2"。
- **journeys ↔ cells**：journey.cells 字段与 cell.yaml 间无双向校验机制（不同于 ADV-06 对 slice contractUsages 的双向校验）。
- **journeys ↔ actors.yaml**：完全断连。actors.yaml 的 maxConsistencyLevel 是 contract 级约束，journey 不感知。
- **journeys ↔ fixtures/**：CLAUDE.md 文档承诺 vs 实际仅 .gitkeep——属于"文档先于代码"的违规（已在用户记忆 `feedback_rules_follow_code` 中明示禁止）。

## 5. 一句话结论

journeys/ 层 schema 简洁且 checkRef 命名编译路径扎实（`ref.go:52` 强匹配 + `runner.go:310` ZeroMatch 拦截），但 **active lifecycle 全空导致 CI 静默跳过、fixtures 文档与实现脱节、status-board 双轨状态、actor 维度缺失** 四个系统级缺口共同表明：journey 形态层尚未收敛到"唯一可推"，建议优先关闭 P0 两条后，再决策 actor 字段与 fixtures 去留。
