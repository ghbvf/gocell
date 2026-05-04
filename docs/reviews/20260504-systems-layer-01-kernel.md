# GoCell 系统工程逐层审查 — kernel/ 层

| 字段 | 值 |
|------|----|
| 评估日期 | 2026-05-04 |
| 基准 commit | `11600a4f` |
| 评估范围 | `kernel/` 22 个一级子模块（assembly / cell / clock / command / contract / crypto / ctxkeys / depgraph / governance / idempotency / journey / lifecycle / metadata / observability / outbox / persistence / registry / scaffold / verify / worker / wrapper） |
| 选定维度 | ① 边界与接口 / ③ 依赖方向与耦合 / ⑧ 守卫与治理 / ⑩ 第一性原理推导 |

## 0. 摘要

kernel/ 层整体处于 **⚠️ 部分具备** 状态。LAYER-01 已迁 depguard（list-mode strict + 白名单），分层公理刚性强；kernel coverage gate ≥90% 实测在 CI 落地。主要缺口集中在 **kernel 内部子模块边界守卫缺失**（22 个 sub-package 之间无强制 DAG 约束）、**`kernel/observability/` 无包文档**（探索阶段已确认），以及 **`metadata.ContractMeta` 与 `wrapper.ContractSpec` 同概念双定义**（一个治理用、一个运行时用，无强制对齐工具）。第 10 维度推导显示当前 22 子模块基本可从公理推得，但 contract / metadata / governance 三者切分在 contract 这个公理点上有一处任意性。

## 1. 评级表

| 维度 | 评级 | 一句话依据 |
|------|------|-----------|
| ① 边界与接口 | ⚠️ 部分具备 | 子模块多有 doc.go 描述对外语义，但 `kernel/observability/` 无包文档；`metadata.ContractMeta` 与 `wrapper.ContractSpec` 同概念两套字段集，无静态一致性守卫 |
| ③ 依赖方向与耦合 | ⚠️ 部分具备 | LAYER-01 由 depguard `kernel-isolation` 强制（`.golangci.yml:133-144`）；assembly→cell/clock/observability 子模块依赖清晰，但 22 子模块之间 *内部 DAG* 无 archtest/depguard 守卫 |
| ⑧ 守卫与治理 | ✅ 已具备 | depguard 8 条 isolation + archtest LAYER-05..10 + kernel coverage gate ≥90%（`_build-lint.yml:182-205`） + governance 6 系列规则（REF/TOPO/VERIFY/FMT/ADV/OUTGARD）覆盖元数据校验闭环 |
| ⑩ 第一性原理推导 | ⚠️ 部分具备 | 22 子模块绝大多数可由"L0-L4 一致性 + Cell 模型 + 治理需要"三公理推得；contract / metadata 切分存在一处任意性（详见 §3） |

## 2. 问题清单

#### [P1] kernel 内部 22 子模块缺乏依赖方向守卫
- **维度**：③ 依赖方向与耦合
- **位置**：`.golangci.yml:133-144` `kernel-isolation` 规则只把 kernel 当一个整体黑盒
- **复杂度**：Cx2（新增 archtest 测试 + 文档化预期 DAG）
- **现象**：depguard `kernel-isolation` 仅约束 kernel 不外泄到 runtime/adapters/cells，对 kernel **内部** 22 子模块之间的依赖方向没有任何静态守卫。assembly 已合法依赖 cell/clock/observability/metrics 等 11 个子模块（`kernel/assembly/assembly.go:23-26`），但若日后某个低层（比如 `crypto`）反向 import `assembly`，CI 不会拦截。kernel 是底座，下游所有层都受其影响，*kernel 自身 DAG 反转* 是高杠杆失误。
- **建议方向**：在 `tools/archtest/` 新增 KERNEL-INTERNAL-DAG-01，固化已知合法上游依赖（assembly/wrapper 在顶层，crypto/clock/ctxkeys 在叶子）。

#### [P1] `kernel/observability/` 无包级文档
- **维度**：① 边界与接口
- **位置**：`kernel/observability/`（无 `doc.go`）；仅 `kernel/observability/metrics/metrics.go:1-12` 有子包 doc
- **复杂度**：Cx1（单文件）
- **现象**：`kernel/observability/` 是 metrics/tracing 的命名空间根，被 `kernel/assembly/assembly.go:25` 等多处导入，但根目录无 `doc.go` 阐明它与下游 `runtime/observability/` 的职责切分。包级文档是 kernel 对外接口语义的第一道说明，缺失会让读者必须从子包或 ADR 反推架构意图。
- **建议方向**：补一个 30-50 行 `doc.go`，明确"kernel/observability 只定义 provider-neutral 抽象（Provider/Collector/Tracer），具体导出器在 adapters/runtime"。

#### [P1] `ContractMeta`（治理）与 `ContractSpec`（运行时）双定义，无对齐守卫
- **维度**：① 边界与接口
- **位置**：`kernel/metadata/types.go:151-178` `ContractMeta` vs `kernel/wrapper/spec.go:21-30` `ContractSpec`
- **复杂度**：Cx2（跨模块 + archtest 一致性测试）
- **现象**：同一"contract"概念在 kernel 内部有两套结构：
  - `metadata.ContractMeta` — 解析自 `contract.yaml`，含 Kind/OwnerCell/Endpoints/Lifecycle/SchemaRefs/Triggers/IdempotencyKey 等治理字段
  - `wrapper.ContractSpec` — Cell 在代码里硬编码并传给 `auth.Mount` / `eventrouter.Subscribe`，含 ID/Kind/Transport/Method/Path/Topic
  注释说 FMT-17 governance rule 做交叉校验（`spec.go:11-13`），但 wrapper 没有任何编译期/import 期机制确保 `ContractSpec.ID` 必然出现在 `ContractMeta.Triggers`/`Endpoints` 中。两套字段集在 PR 评审中容易漂移。
- **建议方向**：要么将 ContractSpec 弱化为 ContractMeta 的 runtime 投影（构造函数从 ContractMeta 派生）；要么强化 archtest，把 FMT-17 反向校验也放到 archtest（已有的是 governance 单向）。

#### [P2] `kernel/metadata` 同时承担"YAML 反序列化模型"与"Schema 守卫"双重责任
- **维度**：⑧ 守卫与治理
- **位置**：`kernel/metadata/types.go` 全文 + `kernel/metadata/parser.go:27-40`
- **复杂度**：Cx3（涉及 governance/cmd/codegen 的导入边界讨论）
- **现象**：`metadata` 包既是被 governance 校验的"被动数据结构"，又承载 `goStructName` 等 codegen schema 扩展（`types.go:29-35`），导致 kernel 公理"kernel 不知道 codegen"被局部破坏——kernel 自己声明了一个只有 codegen 才解释的字段。这是冗余补丁信号（第 10 维度反证）。
- **建议方向**：要么把 codegen-only 字段挪到 `tools/codegen` 自有的 schema overlay；要么明确 `metadata` 包就是"YAML schema 总账本"，在 doc.go 注明它故意承载多消费方所需字段。

## 3. 第 10 维度专项推导

**公理集**（GoCell 第一性原理）：
1. **A1：Cell 是边界单元** → 必然推出 `cell` / `slice` 类型 + lifecycle hooks → 子模块 `cell` / `lifecycle` 必然存在
2. **A2：跨 Cell 通信只通过 contract** → 必然推出 contract 模型 + 运行时 spec → `metadata`(yaml) + `wrapper`(spec) **可二选一**，当前选两者并存
3. **A3：一致性等级 L0-L4 是元数据，不是行为** → L2 outbox 必有"接口在 kernel，实现在 adapters" → `outbox` / `idempotency` / `persistence` 必然存在
4. **A4：assembly 是组合根** → 需要清单 + 启停 + 时钟 + 度量 → `assembly` / `clock` / `observability` 必然存在
5. **A5：声明优于运行时**（GoCell 与 K8s 共宪法） → `metadata` + `governance` + `verify` + `journey` + `depgraph` 必然存在
6. **A6：CLI 工具也是 first-class** → `scaffold` 必然存在

**反证：拿掉 `wrapper` 会怎样？** Cell 注册 HTTP/event 时必须把 contract id 与 Span/Tracer 绑定，这部分代码必然要落在 kernel（因为 cells 不能依赖 adapters）。`wrapper` 不可移除，但 *也可* 与 `contract` 合并——当前 `wrapper.ContractSpec` 的存在是因为 `metadata.ContractMeta` 在 yaml 层、`ContractSpec` 在 Go 字面量层，两者硬编码同步靠 governance FMT-17。这是任意性切分的**轻度实例**，不动摇公理但增加心智成本。

**反证：拿掉 `depgraph` 会怎样？** archtest LAYER-05..10 都建立在 `tools/depgraph` 上（见 `tools/archtest/doc.go:1-7`），但 kernel/depgraph 是其底层抽象。kernel/depgraph 提供 `FromNodes` / Stats / 闭包遍历，是治理工具的不可约依赖。**不可移除**，验证了 A5 公理。

**结论**：22 子模块大约 19 个直接对应公理，3 个（contract 概念分散在 metadata + wrapper + governance）存在**轻度任意性**——不冲突公理，但合并空间存在。

## 4. 跨层观察

- **kernel ↔ runtime**：`kernel/crypto/doc.go:8-11` 明确说明 runtime/crypto 用 type alias 让实现类型对齐 kernel 接口，这是健康的"kernel 定义抽象、runtime 提供具体编排"模式。同样模式存在于 outbox/command。但 LAYER-01 只阻止 kernel→runtime 反向 import，**没有强制 runtime 必须 alias 而不是 redefine**——runtime 可以偷偷新建一个 type，下游消费方混用就分裂。
- **kernel ↔ cells**：cells 通过 `kernel/wrapper.ContractSpec` 字面量声明 contract，governance ADV-06 / VERIFY-01 双向校验 contractUsages ↔ endpoints.subscribers ↔ verify.contract（见 `.claude/rules/gocell/eventbus.md`）。这是健康的"代码事实 ↔ 元数据事实"双向闭环。
- **kernel ↔ adapters**：`kernel/persistence/tx.go:12-14` 这种"接口在 kernel，实现在 adapters"是教科书级 ports & adapters。adapters-isolation depguard 规则同时允许 kernel + runtime + 外部 SDK，符合预期。
- **kernel coverage gate**：`_build-lint.yml:182-205` 用 awk 解析 `go test -cover` 输出并以 90% 为门槛，硬性。豁免名单（outboxtest / celltest / commandtest）合理——这些都是 *test helper* 子包（`commandtest.InMemQueue` 见 `kernel/command/doc.go:50-54`），其覆盖率本就是 indirect。

## 5. 一句话结论

kernel 层公理已基本立住，外层守卫刚性强；下一步杠杆点是 **kernel 内部 22 子模块 DAG 的静态守卫** 与 **`ContractMeta`/`ContractSpec` 双定义的合一**——前者防止底座反转，后者消除最后一处可观测的任意性切分。
