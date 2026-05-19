# Journeys — 业务验收规格

## 定义

Journey 是**业务/产品视角的端到端验收**。每个 `J-*.yaml` 必须对应一个能由产品/业务方陈述的具体业务场景，例如：

- "新用户首次登录可以成功签发 session"（J-useronboarding）
- "连续失败登录后账户自动锁定，管理员可手动解锁"（J-accountlockout）
- "登录事件落入审计 hash chain 且可查询"（J-auditlogintrail）

每条 `passCriteria` 必须对应该业务场景下的具体可观测断言。

## 与其他验收体系的边界

| 体系 | 验收语义 | 单位 | 视角 | 归属判断 |
|------|---------|------|------|---------|
| **journey** | 业务场景端到端可用 | `passCriteria` | 业务方 / 产品 / 终端用户 | 产品方能用业务语言说出"用户能 X" / "系统在 Y 场景能保持 Z" |
| **archtest** | framework 不变量 / 架构约束 | `INVARIANT: <ID>` rule | 架构维护者 | 约束是关于代码/调用关系/类型结构本身，与业务无关 |
| **cell conformance test** | cell 公开 interface 契约 | 一组 case 跑遍所有实现 | 下游 cell consumer | 约束是关于某 interface 的所有实现都必须满足的行为契约 |
| **pkg unit test** | 共享工具行为正确 | table-driven | 任何 import 该 pkg 的代码 | 测的是 pkg/ 层公开函数的输入→输出正确性 |

## 反模式（禁止作为 journey 立项）

1. **pkg layer behavior verification** — 测 `pkg/httputil.WriteError` / `pkg/errcode.Error.MarshalJSON` / `pkg/redaction.RedactSlogAttr` 等共享工具行为
2. **framework invariant** — 测 envelope shape / PII safety / dependency direction 等架构约束
3. **archtest 守卫的运行时重复** — 已有 archtest 静态守的约束不应再以 journey criterion 形式跑 runtime smoke
4. **interface conformance** — 测某 interface 所有实现的行为一致性

判定方法：候选 criterion 的主谓宾不含业务实体词（user / session / config / role / policy / device / order / audit / event 等），大概率不是 journey。即使含业务实体词，若 criterion 的核心断言是关于序列化 / 协议 / 类型系统本身（而非用户可感知的业务结果），仍应归类为 framework invariant — 例如 "envelope 正确序列化后 session 能恢复" 含 session 但实质测序列化，归 archtest。

## Lifecycle 状态机

- `experimental` — 设计中 / 测试覆盖未稳定 — `board.state` 必须 `todo`
- `active` — 至少 1 个 mode:auto criterion + 有 docker-free seam — `board.state` 可 `doing` 或 `done`
- `deprecated` — 业务场景废弃 / 被更细粒度 journey 替代 — `board.state` 不约束；yaml 文件保留作历史记录，`contracts:` 保持以满足 `JOURNEY-CONTRACT-EXISTENCE-01`（若该 contract 仍 active），status-board entry 可保留或删除

由 `kernel/governance/rules_journey.go` 中的 `validateJOURNEYSTATUSLIFECYCLE01` 强制（rule ID: `JOURNEY-STATUS-LIFECYCLE-01`）。

## passCriteria mode

- `mode: auto` — 必须有 `checkRef: journey.<id>.<suffix>`；当 lifecycle 进入 active 时，该 checkRef 必须解析到 `tests/integration/` 内 `TestJ<CamelCase(id)><CamelCase(suffix)>` 测试，`-tags=integration` 跑通，docker-free。experimental 阶段允许 checkRef 对应测试尚未实现（由 VERIFY-06 仅对 active journey 强制守护）
- `mode: manual` — 必须 inline 注释说明为什么不能 auto + 引用 backlog ID 跟踪升级路径

mode:manual 合法理由：
- 架构上不可达 docker-free seam — 必须 backlog 引用 unblock 条目
- 由更强的静态守完全替代时**应删除该 criterion**，而非长留 manual；若 archtest 仅部分覆盖（如只覆盖单一调用点）保留 manual 并 inline 引用 archtest rule ID + 说明剩余覆盖缺口

mode:manual 非合法理由：
- "现在没时间写" / "测试复杂" / "等业务完整后再补"（这种情况应当 lifecycle: experimental）

## 新建 journey 前自查

1. 这是不是真正的业务场景？产品方能用一句话说出来吗？
2. passCriteria 主谓宾是不是业务实体？
3. 是否已有 archtest / cell conformance / pkg unit test 覆盖？如有，不要重复
4. 每个 mode:auto criterion 的 seam 在 docker-free 下可达吗？参考 `.claude/rules/gocell/cell-patterns.md` 中 criterion semantic form → seam 选择表（待 plan 044 §5 落地）

## 现有 journey 一览

见 `journeys/status-board.yaml`。每条 lifecycle / state / 修复路径见 `docs/backlog/cap-14-tooling.md` 中 `JOURNEY-*` 系列条目。
