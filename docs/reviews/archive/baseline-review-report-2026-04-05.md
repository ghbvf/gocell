# 全仓基线审查报告

- 日期：2026-04-05
- 审查对象：项目根目录
- 审查计划：[`docs/architecture/code-review-baseline-plan.md`](../architecture/code-review-baseline-plan.md)
- 最终结论：`blocked`

## 1. 执行摘要

本次审查按既定计划完整执行了 7 个阶段；每个阶段都启动了 6 个角色视角的子审查流，分别覆盖：

- 架构师
- 领域专家
- 工具工程师
- DX
- 魔鬼代言人
- PM

由于会话内并发子 agent 数量有限，阶段之间按顺序推进，但每个阶段都在收齐 6 角色结论后才进入下一阶段。

最终结论一致：当前仓库不具备通过基线审查的条件。核心原因不是单点 bug，而是四类系统性问题叠加：

1. source of truth 断裂
2. metadata/project graph 非 canonical
3. verify/check/generate 存在 false-green
4. 骨架期 runtime 的 lifecycle / state / API boundary 仍有当前阶段缺陷

口径修正：

- 本次仓库更接近 `Phase 0-1` 的基础骨架阶段，而不是 `Phase 2-4` 的可执行/可验证/可交付阶段
- 因此，`outbox` / `idempotency` 仅为接口、`runtime/` 为空、`verify` 为占位符，这些内容本身不自动构成当前阶段 bug
- 真正需要计入缺陷的，是“当前阶段目标内的不一致或误导”：例如资产已红灯、parser 丢失 canonical truth、规则前后矛盾、占位能力却对外表现为可用

## 2. 审查范围

本轮覆盖以下范围：

- 基础原语与运行时：`pkg/*`、`kernel/cell`、`kernel/idempotency`、`kernel/outbox`、`kernel/assembly`、`gocell.go`
- 元数据模型与解析：`kernel/metadata`、schemas、`actors.yaml`
- 治理与查询：`kernel/governance`、`kernel/registry`、`kernel/journey`、`kernel/slice`
- CLI / scaffold / generate：`cmd/gocell`、`kernel/scaffold`、assembly 生成模板
- 资产层：`cells/*`、`contracts/*`、`journeys/*`、`assemblies/*`

低优先级占位目录 `adapters/`、`runtime/`、`examples/` 只做完整性观察，不作为本轮主结论来源。

## 3. 基线命令与结果

### 3.1 通过的命令

```bash
cd src && go test ./...
```

结果：通过。

说明：该结果只能视为信息性信号，不能代表仓库整体健康；后续审查发现 `verify`、`check`、`generate` 等路径存在明显 false-green。

### 3.2 失败的命令

```bash
cd src && go run ./cmd/gocell validate
```

结果：失败，共 6 个阻塞问题：

- `http.auth.me.v1` 缺失 `request.schema.json`
- `http.auth.me.v1` 缺失 `response.schema.json`
- `http.config.flags.v1` 缺失 `request.schema.json`
- `http.config.flags.v1` 缺失 `response.schema.json`
- `access-core/session-refresh` 与 `http.auth.login.v1` 的 consumer 拓扑不一致
- `config-core/config-subscribe` 与 `event.config.changed.v1` 的 subscriber 拓扑不一致

这意味着当前 checked-in 元数据本身就不是自洽状态。

## 4. 阶段执行结果

| 阶段 | 范围 | 六角色状态 | 阶段结论 |
| --- | --- | --- | --- |
| 0 | 基线与拆分 | 已收齐 | 基线阻塞；文档、工具、生成物、运行时边界不一致 |
| 1 | 基础原语与运行时 | 已收齐 | `Assembly` 生命周期和 `BaseCell` 约束过弱；outbox/idempotency 目前仍是接口占位 |
| 2 | 元数据模型与解析 | 已收齐 | parser 不消费嵌入 schema，重复 ID 静默覆盖，path truth/provenance 丢失 |
| 3 | 治理与查询 | 已收齐 | verify 语义不闭合，wildcard/waiver/selector 规则互相打架 |
| 4 | scaffold / generate / CLI | 已收齐 | 占位命令以成功语义暴露，`generate assembly` 路径与 assembly 元数据不一致 |
| 5 | 资产层与样例 | 已收齐 | assets 图谱存在真实冲突，`validate` 红灯来自真实仓库资产 |
| 6 | 跨阶段合流 | 已收齐 | 六角色一致给出 `blocked` signoff |

### 4.1 记录方式说明

本报告当前采用的是“阶段汇总 + 根因合并”写法，而不是把 7 个阶段 × 6 个角色的全部原始输出逐条原样贴出。

这意味着两件事同时成立：

- 没有只做 1 轮局部 review；36 个审查格子都实际执行了
- 当前落盘版本对重复问题做了合并，因此它是审查报告，不是逐格审计台账

所以，“为什么只有这么几条问题”并不意味着只审出了这么几条，而是因为大量角色结论收敛到了同一批根因簇。六角色方法的价值主要在于交叉验证、补盲和提升置信度，不在于强行制造 36 份互不重复的问题单。

如果把要求提升为“严格逐格留痕”，则当前版本仍不够细；因此下面补充每个阶段的问题台账，显式记录阶段内发现，避免问题在汇总时被压扁。

## 5. 阶段问题台账

### 5.1 阶段 0：基线与拆分

- 文档、运行时、CLI、生成链路对仓库当前能力的描述不一致，导致审查起点本身就模糊
- `validate` 是红灯，但 `go test ./...` 是绿灯，仓库没有单一可信总门禁
- `verify` 不是实际执行器，只是声明展示
- generation 路径与 assembly 入口约定不一致
- 运行时最上层 API 和 metadata/governance 之间缺少清晰边界
- 仓库没有一个被工具和文档同时承认的 canonical review unit inventory

### 5.2 阶段 1：基础原语与运行时

- `Assembly` 注册和启动的状态模型过弱，存在注册/启动并发与生命周期不闭合问题
- `Assembly` 对 cells 集合的暴露和使用方式不利于后续安全收口
- `BaseCell` 生命周期过于宽松，错误顺序和重复调用难以暴露
- `outbox` 与 `idempotency` 当前仅提供接口壳；按 `Phase 0-1` 这属于阶段内占位，不单独计为当前 bug
- 真正的问题在于阶段边界没有被清楚表达，容易让人把这些接口误读成已收敛能力
- 顶层 [`gocell.go`](../../gocell.go) 仍直接泄漏 assembly 级别细节，API 过薄

### 5.3 阶段 2：元数据模型与解析

- parser 不消费嵌入 JSON Schema，metadata 只做 YAML 反序列化，不做真正 schema enforcement
- path truth 与 YAML truth 没有 canonicalize，身份锚点可能漂移
- duplicate ID 静默覆盖，后写覆盖前写
- source provenance 未作为 project graph 一等信息保留
- 文档里声明的派生字段默认值，如 `belongsToCell`、`ownerCell`，并未可靠推导
- schemas 已嵌入，但解析链路没有真正使用这些 schema 作为约束

### 5.4 阶段 3：治理与查询

- `VERIFY-01` 对 verify/waiver 闭包的约束不完整
- 非法或空的 waiver 过期值可能同时产生“算覆盖”和“报错误”的矛盾语义
- `REF-14` 接受 `*` consumer wildcard，但 `TOPO-03` 没有一致处理
- `TargetSelector` 只覆盖 `cells/` 和 `contracts/`，遗漏 `journeys/`、`assemblies/`
- query/index 层结果稳定性不足，建立在非 canonical graph 上
- [`kernel/slice/verify.go`](../../kernel/slice/verify.go) 的执行路径未真正成为 metadata-governed 的 CLI 主路径
- 部分诊断如 provenance 缺失类问题在错误信息上仍然偏抽象

### 5.5 阶段 4：scaffold / generate / CLI

- `verify` 只打印目标，不真正执行验证；这在 `Phase 0-1` 可接受，但当前问题是命令形态、退出码和测试会让人误以为能力已就绪
- `check` 的多个子命令仍是 placeholder 但返回成功；问题不在“未实现”，而在“未实现但看起来成功”
- `generate indexes` / `generate boundaries` 仍是 placeholder 但返回成功；同样属于 phase labeling defect，而非单纯缺功能
- `generate assembly` 不遵守 `assembly.build.entrypoint`
- assembly 生成模板仍包含 TODO 风格的 imports/registration 空位
- [`cmd/core-bundle/main.go`](../../cmd/core-bundle/main.go) 仍是空 stub；空 stub 本身不是 bug，但它与文档和生成链路一起会制造错误预期
- journey scaffold 生成的文件命名和内部 `id` 之间可能立即不一致
- projection scaffold 缺少必需字段 `replayable`
- HTTP contract scaffold 会生成 schema 引用，但不生成对应 schema 文件
- root discovery 依赖向上寻找 `go.mod`，对 repo root / tool root 关系处理不清
- scaffold ID 通过原始 `filepath.Join` 进入路径，存在路径穿越风险

### 5.6 阶段 5：资产层与样例

- `validate` 失败的 6 个问题已在真实资产上复现，不是测试夹具问题
- [`assemblies/core-bundle/generated/boundary.yaml`](../../assemblies/core-bundle/generated/boundary.yaml) 明显陈旧、占位化且不可信
- `session-refresh` 把 refresh 流建模为调用 `http.auth.login.v1`，与 contract client 声明冲突
- `config-subscribe` 与 `event.config.changed.v1` 的 subscriber 声明冲突
- `audit-core` 在 journeys / contracts 中被宣称会接收某些事件，但对应 slice 使用痕迹不足
- config 变更/回滚链路在 journeys、contracts、slices 之间讲述的 subscriber 集合不一致
- `J-sso-login` 等 journey 省略了关键跨 cell 依赖，如 `http.config.get.v1`
- 多数 schema 文件仍是近乎空壳的 `type: object`，即使补齐缺失文件，约束仍然很弱
- `actors.yaml` 对已声明旅程的支撑度不足，actor map 过 sparse

### 5.7 阶段 6：跨阶段合流

- 六角色结论一致为 `blocked`
- 高重复度问题被收敛为四个根因簇，而不是继续以文件级碎片罗列
- 各阶段问题之间存在明确依赖顺序，不能平铺并行修
- 当前总报告更适合作为决策材料，不足以替代逐格审计台账

## 6. 汇总 Findings

### 6.1 P0

#### P0-1 当前仓库基线不成立

`validate` 直接失败，说明仓库当前元数据、contract 资产和 topology 声明并不自洽。这个问题先于任何运行时设计争论，因为它已经阻止了“当前仓库是否有效”这一最基本判断。

主要证据：

- [`cmd/gocell/validate.go`](../../cmd/gocell/validate.go)
- [`contracts/http/auth/me/v1/contract.yaml`](../../contracts/http/auth/me/v1/contract.yaml)
- [`contracts/http/config/flags/v1/contract.yaml`](../../contracts/http/config/flags/v1/contract.yaml)
- [`cells/access-core/slices/session-refresh/slice.yaml`](../../cells/access-core/slices/session-refresh/slice.yaml)
- [`cells/config-core/slices/config-subscribe/slice.yaml`](../../cells/config-core/slices/config-subscribe/slice.yaml)

影响：

- 无法把当前仓库当作通过基线审查的“参考真相”
- 所有基于现状继续生成、验证、演示的动作都建立在红灯状态上

#### P0-2 工具链存在 false-green，`go test ./...` 不是可信总门禁

CLI 当前允许多个“看起来成功但实际上没有完成约定工作”的路径：

- `verify` 只打印声明目标并退出 0
- `check` 的占位子命令直接成功
- `generate indexes` / `generate boundaries` 占位成功
- `generate assembly` 成功，但落盘路径不服从 `assembly.build.entrypoint` 和 `generated/boundary.yaml`
- 测试仍显式接受上述占位成功行为

这里的缺陷不是“骨架期存在占位符”，而是“占位符以成功语义出现，并与文档、命令名、测试绿灯共同制造 readiness 幻觉”。

主要证据：

- [`cmd/gocell/verify.go`](../../cmd/gocell/verify.go)
- [`cmd/gocell/check.go`](../../cmd/gocell/check.go)
- [`cmd/gocell/generate.go`](../../cmd/gocell/generate.go)
- [`cmd/gocell/main_test.go`](../../cmd/gocell/main_test.go)
- [`assemblies/core-bundle/assembly.yaml`](../../assemblies/core-bundle/assembly.yaml)
- [`cmd/core-bundle/main.go`](../../cmd/core-bundle/main.go)
- [`assemblies/core-bundle/generated/boundary.yaml`](../../assemblies/core-bundle/generated/boundary.yaml)

影响：

- `go test ./...` 通过不能代表 repo readiness
- 使用者会误以为 verify/check/generate 已经形成闭环

### 6.2 P1

#### P1-1 Metadata parser / project graph 不是 canonical graph

当前 parser 直接 `yaml.Unmarshal` 到结构并写入 map：

- 不执行嵌入 JSON Schema 校验
- 重复 ID 后写覆盖前写
- source provenance 没有作为 graph 一等信息保留
- 文档中声明的 path-derived identity 没有真正成为唯一身份锚点

主要证据：

- [`kernel/metadata/parser.go`](../../kernel/metadata/parser.go)
- [`kernel/metadata/schemas/embed.go`](../../kernel/metadata/schemas/embed.go)
- [`docs/architecture/metadata-model-v3.md`](../architecture/metadata-model-v3.md)

影响：

- 阶段 2 的类型/路径/schema 约束无法真正落地
- 阶段 3 的规则、索引和 target selection 都建立在松散输入上

#### P1-2 Governance / verify 语义前后不一致

当前治理层存在多个互相冲突的行为：

- waiver 的空值/非法值可能同时“算覆盖”又“报错误”
- `REF-14` 接受 `*` consumer wildcard，但 `TOPO-03` 仍会将其判错
- `TargetSelector` 只理解 `cells/` 和 `contracts/`，遗漏 `journeys/` 与 `assemblies/`
- verify runner 与 CLI 路径没有真正打通

主要证据：

- [`kernel/governance/rules_verify.go`](../../kernel/governance/rules_verify.go)
- [`kernel/governance/rules_ref.go`](../../kernel/governance/rules_ref.go)
- [`kernel/governance/rules_topo.go`](../../kernel/governance/rules_topo.go)
- [`kernel/governance/targets.go`](../../kernel/governance/targets.go)
- [`kernel/slice/verify.go`](../../kernel/slice/verify.go)

影响：

- 当前 verify 不能被视为“可执行治理证明”
- 影响选择与规则结论会漏检或误判

#### P1-3 当前阶段真正的运行时问题应收敛到 lifecycle / state / API boundary

阶段 1 六角色审查后，原报告把“骨架期未实现”与“当前阶段缺陷”混在了一起。按 `Phase 0-1` 重分后，真正应计入当前阶段问题的是：

- `Assembly` 注册/启动状态机有竞争窗口
- `BaseCell` 生命周期约束过弱
- 顶层 `gocell.NewAssembly` 仍在泄漏 assembly 级别细节

而以下内容不应直接按 bug 记账：

- `outbox` / `idempotency` 只有接口，没有完整语义实现
- `runtime/` 目录当前为空

这些更适合作为 roadmap gap 或阶段外能力缺口，前提是文档和 CLI 不把它们表述成已交付能力。

主要证据：

- [`kernel/assembly/assembly.go`](../../kernel/assembly/assembly.go)
- [`kernel/cell/base.go`](../../kernel/cell/base.go)
- [`kernel/outbox/outbox.go`](../../kernel/outbox/outbox.go)
- [`kernel/idempotency/idempotency.go`](../../kernel/idempotency/idempotency.go)
- [`gocell.go`](../../gocell.go)

影响：

- 评审口径需要与项目阶段一致，否则会把正常的骨架期占位误判为缺陷
- 当前阶段仍需修的是 lifecycle / state / boundary 的基础正确性，而不是提前补齐所有 Phase 2-4 能力

#### P1-4 对外叙事超前于代码现实

文档和 README 目前更像是在描述“可运行框架 + 内建 cell 生态”，但仓库现实更接近“metadata/governance prototype”：

- quick start 引用了仓库中不存在的包
- 目录树展示让人误以为 `runtime/`、`adapters/`、`examples/` 已有实质内容
- `src/cells` 当前主要是 metadata，而不是可直接运行的内建实现

主要证据：

- [`README.md`](../../README.md)
- [`docs/architecture/overview.md`](../architecture/overview.md)
- [`cells/access-core/cell.yaml`](../../cells/access-core/cell.yaml)

影响：

- reviewer、使用者、未来贡献者会建立错误心智模型
- 生成链路和 CLI 的设计目标也会被错误叙事牵着走

## 7. 根因收敛

阶段 6 的六角色合流后，问题可收敛为四个根因簇：

### 7.1 Fractured Source Of Truth

同一概念在 metadata struct、docs、generated artifact、runtime surface 中重复建模，且没有单一可执行真相。

### 7.2 Non-Canonical Project Graph

解析阶段没有产出经过 schema 校验、带 provenance、拒绝重复、路径锚定一致的 immutable graph。

### 7.3 Verification Is Not Executable Reality

verify/check/generate/测试允许“成功但没做承诺中的工作”的路径，因此现有 green signal 不可信。

### 7.4 Runtime Kernel Is A Leaky Stub

运行时生命周期、状态边界、注入与交付语义仍不足以承载更高层的治理承诺。

## 8. 修复顺序建议

建议按以下顺序处理，而不是平铺并行：

1. 把 `cd src && go run ./cmd/gocell validate` 变成唯一硬门禁，并让 `verify`、`check`、`generate` fail closed
2. 重建 metadata parser，产出 validated + immutable + canonical 的 project graph
3. 修正当前 6 个 validate 阻塞项，并统一 contracts / journeys / slices 的实际关系
4. 让 `generate assembly` 严格服从 `assembly.build.entrypoint`、`generated/boundary.yaml` 等 assembly 约定
5. 把 CLI verify 真正接到 runner，并扩展 target selector 覆盖 `journeys/`、`assemblies/`
6. 再回头硬化 runtime：assembly state machine、BaseCell lifecycle、outbox/idempotency contract、顶层 API 收口
7. 最后重写 README 与外部叙事，只宣称当前仓库真正具备的能力

## 9. 当前可宣称与不可宣称边界

### 9.1 可以宣称

- 一个处于早期阶段的 metadata/governance prototype
- 已有 parser、validator、assembly skeleton、CLI shells
- 已有一套可作为后续收敛对象的 metadata model 文档

### 9.2 不应宣称

- “可运行的完整 framework”
- “内建 cells 已可直接使用”
- “verify/check 已形成真实的验证闭环”
- “当前 README quick start 可代表仓库真实使用路径”

## 10. 本次审查产物

- 审查计划：[`docs/architecture/code-review-baseline-plan.md`](../architecture/code-review-baseline-plan.md)
- 审查报告：`docs/reviews/baseline-review-report-2026-04-05.md`

本次仅产出报告，没有修改仓库代码。

## 11. 证据保全状态

本节用于明确说明：哪些内容是原始留痕，哪些内容是汇总，哪些内容是基于现有材料重建。

### 11.1 A 类：原始角色输出仍可直接追溯

以下内容在当前会话线程中仍保留为原始角色返回，可视为原始留痕：

- 阶段 6 / 架构师
- 阶段 6 / 领域专家
- 阶段 6 / 工具工程师
- 阶段 6 / DX
- 阶段 6 / 魔鬼代言人
- 阶段 6 / PM

这些内容在本报告中已被合并到“阶段 6 合流结论”“汇总 Findings”“根因收敛”中，但其原始文本曾在会话中完整返回。

### 11.2 B 类：仅保留阶段汇总，不再具备逐角色原文

以下内容没有单独落成逐角色原始台账；当前保留下来的，是主线程汇总后的阶段结论：

- 阶段 0 / 六角色汇总
- 阶段 1 / 六角色汇总
- 阶段 2 / 六角色汇总
- 阶段 3 / 六角色汇总
- 阶段 4 / 六角色汇总
- 阶段 5 / 六角色汇总

因此，阶段 0 到阶段 5 目前只能做到“阶段问题台账级”追溯，做不到“逐角色逐条原文级”追溯。

### 11.3 C 类：可复核的命令与文件证据

以下证据不依赖子 agent 原文，仍可由仓库现状直接复核：

- `cd src && go test ./...` 通过
- `cd src && go run ./cmd/gocell validate` 失败，共 6 项
- 报告中引用的代码与资产文件路径

这部分是当前最稳定的客观证据。

### 11.4 结论

当前报告的证据等级是：

- 阶段 6：原始角色输出可追溯
- 阶段 0-5：阶段汇总可追溯，逐角色原文不可追溯
- 命令与文件证据：可直接复核

因此，这份报告可以作为决策级、审查级材料使用，但不能声称自己是“36 个审查格子逐条原文全部保全”的审计级台账。
